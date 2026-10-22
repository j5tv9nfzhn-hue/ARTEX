package server

// 这个文件自带一份最小可用的二维码编码器，只服务一件事：
// 「系统 → 局域网连接」页上那张给手机扫的图（页面见 web/src/app/(main)/system/lan）。
//
// 为什么自己写而不引第三方库：后端依赖清单是硬约束，新增一个只为画图的包要动 go.sum，
// 还会跟着六个平台的打包一起去评审。而这里真正需要的只是「把 http://192.168.x.x:8787
// 这种短字符串编成二维码」，所以只实现规范里最小的那一档：
//
//	字节模式 + 纠错等级 M + 固定掩码 0 + 版本 1..10 自动选择
//
// 掩码固定为 0 是规范允许的（扫描器读得出任意掩码），省掉「四种惩罚分」打分逻辑；
// 版本上限 10 对应 57x57、约 213 字节，远超局域网地址长度，超了直接报错而不是静默出错。
//
// 正确性由 server/qr_test.go 的黄金矩阵守着：矩阵来自一份已被真实解码器
// （zxing-cpp）验证过的参考实现，任何一步写错都会让测试红掉。

import (
	"fmt"
	"strings"
)

const (
	// qrECCFormatM 是格式信息里纠错等级 M 的编码位：L=1、M=0、Q=3、H=2。
	qrECCFormatM = 0
	// qrMaskFixed 固定使用掩码 0（列号与行号之和为偶数处翻转）。
	qrMaskFixed = 0
	// qrMaxVersion 是本实现支持到的版本：10 → 57x57。
	qrMaxVersion = 10
)

// qrECPerBlockM 是纠错等级 M 下每个版本「每块的纠错码字数」，下标是版本号。
var qrECPerBlockM = [qrMaxVersion + 1]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26}

// qrNumBlocksM 是纠错等级 M 下每个版本的「分块数」，下标是版本号。
var qrNumBlocksM = [qrMaxVersion + 1]int{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5}

// qrCountBits 返回字节模式下长度字段占几位：版本 1..9 是 8 位，10 及以上是 16 位。
func qrCountBits(ver int) int {
	if ver < 10 {
		return 8
	}
	return 16
}

// qrRawDataModules 返回该版本里能放数据的模块数（已扣掉定位、校正、格式、版本等固定图形）。
func qrRawDataModules(ver int) int {
	result := (16*ver+128)*ver + 64
	if ver >= 2 {
		numAlign := ver/7 + 2
		result -= (25*numAlign-10)*numAlign - 55
		if ver >= 7 {
			result -= 36
		}
	}
	return result
}

// qrDataCodewords 返回该版本纠错等级 M 下的数据码字数（不含纠错码字）。
func qrDataCodewords(ver int) int {
	return qrRawDataModules(ver)/8 - qrECPerBlockM[ver]*qrNumBlocksM[ver]
}

// qrAlignPositions 返回校正图形的行列中心坐标（升序）。
//
// 必须升序：下面画的时候靠「首/尾元素」来判断哪三个角要跳过，
// 乱序会把校正图形盖到三个定位图形上，整个码就废了。
func qrAlignPositions(ver int) []int {
	if ver == 1 {
		return nil
	}
	num := ver/7 + 2
	step := (ver*8 + num*3 + 5) / (num*4 - 4) * 2
	size := ver*4 + 17
	result := make([]int, 0, num)
	result = append(result, 6)
	for i := num - 2; i >= 0; i-- {
		result = append(result, size-7-i*step)
	}
	// 上面是从末位往回推，天然升序（size-7 最大），这里再排一次以防版本步长变化。
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

// qrGFMul 是 GF(2^8)（本原多项式 0x11D）上的乘法。
func qrGFMul(x, y int) int {
	z := 0
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= ((y >> uint(i)) & 1) * x
	}
	return z
}

// qrRSDivisor 生成 Reed-Solomon 的生成多项式系数（次数为 degree）。
func qrRSDivisor(degree int) []int {
	result := make([]int, degree)
	result[degree-1] = 1
	root := 1
	for i := 0; i < degree; i++ {
		for j := 0; j < len(result); j++ {
			result[j] = qrGFMul(result[j], root)
			if j+1 < len(result) {
				result[j] ^= result[j+1]
			}
		}
		root = qrGFMul(root, 0x02)
	}
	return result
}

// qrRSRemainder 用生成多项式除数据，得到该块的纠错码字。
func qrRSRemainder(data []int, divisor []int) []int {
	result := make([]int, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		copy(result, result[1:])
		result[len(result)-1] = 0
		for i, coef := range divisor {
			result[i] ^= qrGFMul(coef, factor)
		}
	}
	return result
}

// qrPickVersion 选出能装下 payload 的最小版本。
func qrPickVersion(n int) (int, error) {
	for ver := 1; ver <= qrMaxVersion; ver++ {
		need := 4 + qrCountBits(ver) + n*8
		if need <= qrDataCodewords(ver)*8 {
			return ver, nil
		}
	}
	return 0, fmt.Errorf("二维码内容过长（%d 字节），最多支持版本 %d", n, qrMaxVersion)
}

// qrCodewords 把 payload 编成最终的码字序列（数据码字 + 纠错码字，已交织）。
func qrCodewords(payload []byte, ver int) []int {
	capacity := qrDataCodewords(ver) * 8
	bits := make([]bool, 0, capacity)
	push := func(val int, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (val>>uint(i))&1 != 0)
		}
	}
	push(0b0100, 4)
	push(len(payload), qrCountBits(ver))
	for _, b := range payload {
		push(int(b), 8)
	}
	// 结束符最多 4 个 0，然后补到字节边界。
	term := capacity - len(bits)
	if term > 4 {
		term = 4
	}
	push(0, term)
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}

	cw := make([]int, 0, qrDataCodewords(ver))
	for i := 0; i < len(bits); i += 8 {
		v := 0
		for j := 0; j < 8; j++ {
			v <<= 1
			if bits[i+j] {
				v |= 1
			}
		}
		cw = append(cw, v)
	}
	// 剩下的位置按规范交替填 0xEC / 0x11。
	for pad := 0xEC; len(cw) < qrDataCodewords(ver); pad ^= 0xEC ^ 0x11 {
		cw = append(cw, pad)
	}

	ecLen := qrECPerBlockM[ver]
	numBlocks := qrNumBlocksM[ver]
	raw := qrRawDataModules(ver) / 8
	numShort := numBlocks - raw%numBlocks
	shortLen := raw / numBlocks
	divisor := qrRSDivisor(ecLen)

	datas := make([][]int, numBlocks)
	eccs := make([][]int, numBlocks)
	k := 0
	for i := 0; i < numBlocks; i++ {
		dlen := shortLen - ecLen
		if i >= numShort {
			dlen++
		}
		datas[i] = append([]int(nil), cw[k:k+dlen]...)
		k += dlen
		eccs[i] = qrRSRemainder(datas[i], divisor)
	}

	out := make([]int, 0, raw)
	for i := 0; i < shortLen-ecLen+1; i++ {
		for j := 0; j < numBlocks; j++ {
			if i < len(datas[j]) {
				out = append(out, datas[j][i])
			}
		}
	}
	for i := 0; i < ecLen; i++ {
		for j := 0; j < numBlocks; j++ {
			out = append(out, eccs[j][i])
		}
	}
	return out
}

// qrMatrix 是编码过程中的画布：mod 是最终颜色，fn 标记「固定图形」——
// 这些位置不参与数据填充，也不受掩码影响。
type qrMatrix struct {
	ver  int
	size int
	mod  [][]bool
	fn   [][]bool
}

func newQRMatrix(ver int) *qrMatrix {
	size := ver*4 + 17
	mod := make([][]bool, size)
	fn := make([][]bool, size)
	for i := 0; i < size; i++ {
		mod[i] = make([]bool, size)
		fn[i] = make([]bool, size)
	}
	return &qrMatrix{ver: ver, size: size, mod: mod, fn: fn}
}

func (m *qrMatrix) setFn(x int, y int, dark bool) {
	m.mod[y][x] = dark
	m.fn[y][x] = true
}

// absInt 只是为了不依赖 max/min 内建函数，保持这段代码在旧工具链上也能读。
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// drawFunctionPatterns 画定位/分隔/定时/校正/格式/版本这些固定图形。
func (m *qrMatrix) drawFunctionPatterns() {
	size := m.size
	for i := 0; i < size; i++ {
		m.setFn(6, i, i%2 == 0)
		m.setFn(i, 6, i%2 == 0)
	}
	corners := [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}}
	for _, c := range corners {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				dist := absInt(dx)
				if absInt(dy) > dist {
					dist = absInt(dy)
				}
				x := c[0] + dx
				y := c[1] + dy
				if x < 0 || y < 0 || x >= size || y >= size {
					continue
				}
				m.setFn(x, y, dist != 2 && dist != 4)
			}
		}
	}
	pos := qrAlignPositions(m.ver)
	n := len(pos)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			// 三个角上已经有定位图形，校正图形要跳过。
			if (i == 0 && j == 0) || (i == 0 && j == n-1) || (i == n-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					dist := absInt(dx)
					if absInt(dy) > dist {
						dist = absInt(dy)
					}
					m.setFn(pos[i]+dx, pos[j]+dy, dist != 1)
				}
			}
		}
	}
	m.drawFormatBits()
	m.drawVersionBits()
}

// drawFormatBits 画纠错等级 + 掩码那 15 位（BCH(15,5)），两个副本都要画。
func (m *qrMatrix) drawFormatBits() {
	size := m.size
	data := qrECCFormatM<<3 | qrMaskFixed
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>uint(i))&1 != 0 }

	for i := 0; i < 6; i++ {
		m.setFn(8, i, bit(i))
	}
	m.setFn(8, 7, bit(6))
	m.setFn(8, 8, bit(7))
	m.setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		m.setFn(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		m.setFn(size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.setFn(8, size-15+i, bit(i))
	}
	// 右下角那个永远不会用到、恒为深色的模块。
	m.setFn(8, size-8, true)
}

// drawVersionBits 画版本信息（BCH(18,6)），只有版本 7 及以上才有。
func (m *qrMatrix) drawVersionBits() {
	if m.ver < 7 {
		return
	}
	rem := m.ver
	for i := 0; i < 12; i++ {
		rem = rem<<1 ^ (rem>>11)*0x1F25
	}
	bits := m.ver<<12 | rem
	size := m.size
	for i := 0; i < 18; i++ {
		dark := (bits>>uint(i))&1 != 0
		a := size - 11 + i%3
		b := i / 3
		m.setFn(a, b, dark)
		m.setFn(b, a, dark)
	}
}

// drawCodewords 按规范从右下角起、两列一组蛇形填数据，跳过所有固定图形。
func (m *qrMatrix) drawCodewords(cw []int) {
	size := m.size
	total := len(cw) * 8
	i := 0
	for right := size - 1; right >= 1; right -= 2 {
		// 第 6 列被定时图形占了，整列跳过。
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = size - 1 - vert
				}
				if m.fn[y][x] || i >= total {
					continue
				}
				m.mod[y][x] = (cw[i>>3]>>uint(7-(i&7)))&1 != 0
				i++
			}
		}
	}
}

// applyMask 套用掩码 0：只翻数据区，固定图形不动。
func (m *qrMatrix) applyMask() {
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			if m.fn[y][x] {
				continue
			}
			if (x+y)%2 == 0 {
				m.mod[y][x] = !m.mod[y][x]
			}
		}
	}
}

// qrEncode 把一段文本编成二维码矩阵。
func qrEncode(text string) (*qrMatrix, error) {
	payload := []byte(text)
	ver, err := qrPickVersion(len(payload))
	if err != nil {
		return nil, err
	}
	m := newQRMatrix(ver)
	m.drawFunctionPatterns()
	m.drawCodewords(qrCodewords(payload, ver))
	m.applyMask()
	return m, nil
}

// QRMatrixRows 把二维码渲染成「每行一串 #/.」的文本，供测试比对黄金矩阵。
func QRMatrixRows(text string) ([]string, error) {
	m, err := qrEncode(text)
	if err != nil {
		return nil, err
	}
	rows := make([]string, m.size)
	for y := 0; y < m.size; y++ {
		var b strings.Builder
		for x := 0; x < m.size; x++ {
			if m.mod[y][x] {
				b.WriteByte('#')
			} else {
				b.WriteByte('.')
			}
		}
		rows[y] = b.String()
	}
	return rows, nil
}

// QRCodeSVG 把一段文本编成二维码的 SVG 图片。
//
// 固定「白底黑块」而不是跟随主题：反色二维码有不少扫码器读不出来，
// 深色界面里请把这张图放在白色卡片上（页面那边就是这么做的）。
func QRCodeSVG(text string) (string, error) {
	m, err := qrEncode(text)
	if err != nil {
		return "", err
	}
	const quiet = 4
	dim := m.size + quiet*2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, dim, dim)
	b.WriteString(`<path fill="#000000" d="`)
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			if m.mod[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}
