//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 本文件提供自举所需的两个底层能力：
//   - 查出某个 TCP 监听端口归哪个进程所有（IP Helper API）
//   - 查出进程的可执行文件路径与结束进程（kernel32）
//
// 两者都刻意避开 netstat 文本解析：netstat 的输出格式会随系统语言变化，
// 且需要额外 fork 出一个进程，在启动早期（此时还没有完全初始化）调用并不可靠。

const (
	// processTerminate / processQueryLimitedInfo 是 PROCESS_ACCESS_RIGHTS 位。
	// 不直接引用 windows 包的常量，减少对具体符号名的依赖。
	processTerminate        = 0x0001
	processQueryLimitedInfo = 0x1000

	// mibTcpStateListen 是 MIB_TCP_STATE_LISTEN，只有处于监听态的条目才占用端口。
	mibTcpStateListen = 2

	// afInet / afInet6 是 ADDRESS_FAMILY。
	// 必须分开查询：用 AF_UNSPEC 时表内 IPv4 行与 IPv6 行长度不同，
	// 无法用固定步长遍历。
	afInet  = 2
	afInet6 = 23

	// tcpTableOwnerPIDListener 是 TCP_TABLE_OWNER_PID_LISTENER，
	// 只返回 LISTEN 状态的连接，体积最小。
	tcpTableOwnerPIDListener = 3
)

// mibTCP4Row 对应 Win32 的 MIB_TCPROW_OWNER_PID（IPv4），固定 24 字节。
// 字段顺序必须与 SDK 定义一致，不能重排。
type mibTCP4Row struct {
	dwState      uint32
	dwLocalAddr  uint32
	dwLocalPort  uint32
	dwRemoteAddr uint32
	dwRemotePort uint32
	dwOwningPid  uint32
}

// mibTCP6Row 对应 MIB_TCP6ROW_OWNER_PID（IPv6），固定 56 字节。
// 注意字段顺序与 IPv4 版完全不同（地址变成 16 字节，且 state/pid 挪到了末尾），
// 绝不能复用 mibTCP4Row。
type mibTCP6Row struct {
	ucLocalAddr    [16]byte
	dwLocalScopeID uint32
	dwLocalPort    uint32
	ucRemoteAddr   [16]byte
	dwRemoteScope  uint32
	dwRemotePort   uint32
	dwState        uint32
	dwOwningPid    uint32
}

// 编译期锁定行大小。
//
// walkTCPTable 是按定长步长在缓冲区里走行的，一旦 Go 结构体的对齐/填充
// 与 Win32 的定义差一个字节，后续所有行都会读到错位字段——不崩溃，
// 只是安静地返回错误的 PID。这两组下标表达式在大小不符时直接编译失败。
var (
	_ [unsafe.Sizeof(mibTCP4Row{}) - 24]byte
	_ [24 - unsafe.Sizeof(mibTCP4Row{})]byte
	_ [unsafe.Sizeof(mibTCP6Row{}) - 56]byte
	_ [56 - unsafe.Sizeof(mibTCP6Row{})]byte
)

var (
	modIPHlpAPI             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = modIPHlpAPI.NewProc("GetExtendedTcpTable")
)

// decodePort 还原 MIB 行里的 dwLocalPort。
//
// 该字段保存的是 htons(端口) 的结果，且被塞在 DWORD 的低 16 位里。
// 因此必须把低 16 位按字节交换一次才是主机序端口——方向弄反会让
// 8787 被读成 21282，端口占用检测随之失效。
func decodePort(v uint32) int {
	return int(v&0xff)<<8 | int((v>>8)&0xff)
}

// walkTCPTable 拉取指定地址族的 TCP 监听表，并对每一行调用 visit，
// 返回 visit 首次返回非 0 时的该值。拉取失败或无匹配时返回 0。
func walkTCPTable(af uintptr, rowSize uint32, visit func(row []byte) uint32) uint32 {
	buf := make([]byte, 8192)
	size := uint32(len(buf))

	for attempt := 0; attempt < 6; attempt++ {
		ret, _, _ := procGetExtendedTCPTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			af,
			uintptr(tcpTableOwnerPIDListener),
			0,
		)

		if ret != 0 {
			// ERROR_INSUFFICIENT_BUFFER：size 已被填成所需字节数，扩容后重试。
			// 若 size 没变大，说明是别的错误，直接放弃。
			if size == 0 || size <= uint32(len(buf)) {
				return 0
			}
			buf = make([]byte, size)
			continue
		}

		// 调用成功，但 Windows 报告需要比实际分配更大的缓冲区（罕见）：
		// 扩容重试，避免解析越界。
		if size > uint32(len(buf)) {
			buf = make([]byte, size)
			continue
		}

		// 前 4 字节是 dwNumEntries，之后是定长数组。
		count := binary.LittleEndian.Uint32(buf[0:4])
		for i := uint32(0); i < count; i++ {
			off := 4 + i*rowSize
			if off+rowSize > uint32(len(buf)) {
				return 0
			}
			if pid := visit(buf[off : off+rowSize]); pid != 0 {
				return pid
			}
		}
		return 0
	}
	return 0
}

// lookupTCP4 在 IPv4 监听表里找占用 port 的进程。
//
// 先找本地地址完全匹配的；找不到再退化到"该端口上的任意监听者"，
// 因为别人用 0.0.0.0:port 监听同样会让我们绑定失败。
func lookupTCP4(want uint32, port int) uint32 {
	rowSize := uint32(unsafe.Sizeof(mibTCP4Row{}))
	var wildcard uint32

	pid := walkTCPTable(afInet, rowSize, func(row []byte) uint32 {
		r := (*mibTCP4Row)(unsafe.Pointer(&row[0]))
		if r.dwState != mibTcpStateListen || decodePort(r.dwLocalPort) != port {
			return 0
		}
		if r.dwLocalAddr == want {
			return r.dwOwningPid
		}
		wildcard = r.dwOwningPid
		return 0
	})
	if pid != 0 {
		return pid
	}
	return wildcard
}

// lookupTCP6 是 lookupTCP4 的 IPv6 版本，遍历 MIB_TCP6ROW_OWNER_PID。
func lookupTCP6(want [16]byte, port int) uint32 {
	rowSize := uint32(unsafe.Sizeof(mibTCP6Row{}))
	var wildcard uint32

	pid := walkTCPTable(afInet6, rowSize, func(row []byte) uint32 {
		r := (*mibTCP6Row)(unsafe.Pointer(&row[0]))
		if r.dwState != mibTcpStateListen || decodePort(r.dwLocalPort) != port {
			return 0
		}
		if r.ucLocalAddr == want {
			return r.dwOwningPid
		}
		wildcard = r.dwOwningPid
		return 0
	})
	if pid != 0 {
		return pid
	}
	return wildcard
}

// lookupTCPTablePID 返回正在监听 address 的进程 PID。
// 端口空闲、或无法判定（地址非法、API 调用失败）时返回 0。
//
// 注意"端口空闲"已经由调用方用 net.Listen 试绑判定过了；这里只负责
// 在确认端口被占用后把占用者找出来。
func lookupTCPTablePID(address string) uint32 {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return 0
	}

	ip := net.ParseIP(host)
	if host != "" && ip == nil {
		// 允许传主机名（如 localhost）
		addrs, lerr := net.LookupIP(host)
		if lerr != nil || len(addrs) == 0 {
			return 0
		}
		ip = addrs[0]
	}

	// net.IP 是 []byte，不能直接转成 [16]byte，必须逐字节拷贝。
	if ip == nil {
		// 空主机名 = 通配监听：先扫 IPv4 表，want 传 0 即 0.0.0.0；
		// IPv4 没命中再扫 IPv6 表。任何一表命中都算端口被占。
		if pid := lookupTCP4(0, port); pid != 0 {
			return pid
		}
		var anyV6 [16]byte
		copy(anyV6[:], net.IPv6zero.To16())
		return lookupTCP6(anyV6, port)
	}

	if v4 := ip.To4(); v4 != nil {
		return lookupTCP4(binary.BigEndian.Uint32(v4), port)
	}
	v16 := ip.To16()
	if v16 == nil {
		return 0
	}
	var want6 [16]byte
	copy(want6[:], v16)
	return lookupTCP6(want6, port)
}

// processImagePath 返回 pid 对应进程的可执行文件完整路径。
//
// QueryFullProcessImageName 需要一个已打开的进程句柄，不能直接传 PID，
// 因此这里先以查询权限打开、用完即关。权限不足时返回错误。
func processImagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(processQueryLimitedInfo, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	if n > uint32(len(buf)) {
		n = uint32(len(buf))
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// terminateProcess 强制结束指定进程。
func terminateProcess(pid uint32) error {
	h, err := windows.OpenProcess(processTerminate|processQueryLimitedInfo, false, pid)
	if err != nil {
		return fmt.Errorf("打开进程 %d 失败：%w", pid, err)
	}
	defer windows.CloseHandle(h)

	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("结束进程 %d 失败：%w", pid, err)
	}
	return nil
}
