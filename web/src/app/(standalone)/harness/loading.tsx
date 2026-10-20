// 路由级骨架：客户端切换 /harness 时先出这个，避免「点了没反应」的空白期。
// 结构与真实三栏一致，切换完成后不会跳版。

export default function HarnessLoading() {
  return (
    <div className="flex h-dvh min-h-0 animate-pulse overflow-hidden bg-background">
      <div className="hidden w-[266px] shrink-0 flex-col gap-2 border-r p-3 md:flex">
        <div className="h-6 w-32 rounded bg-muted" />
        <div className="h-10 rounded-md bg-muted/70" />
        <div className="h-7 rounded-md bg-muted/50" />
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-12 rounded-md bg-muted/40" />
        ))}
      </div>
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex h-11 shrink-0 items-center gap-2 border-b px-3">
          <div className="h-3 w-3 rounded bg-muted" />
          <div className="h-4 w-40 rounded bg-muted" />
          <div className="ml-auto h-6 w-24 rounded-md bg-muted/60" />
        </div>
        <div className="flex flex-1 flex-col gap-3 p-6">
          <div className="ml-auto h-9 w-2/5 rounded-xl bg-muted/60" />
          <div className="h-4 w-3/4 rounded bg-muted/40" />
          <div className="h-4 w-2/3 rounded bg-muted/40" />
          <div className="h-24 w-full rounded-lg bg-muted/30" />
          <div className="h-4 w-1/2 rounded bg-muted/40" />
        </div>
        <div className="mx-auto mb-3 h-24 w-[calc(100%-3rem)] rounded-xl border bg-muted/20" />
      </div>
      <div className="hidden w-[372px] shrink-0 flex-col gap-2 border-l p-3 xl:flex">
        <div className="h-7 w-40 rounded-md bg-muted/60" />
        <div className="h-40 rounded-md bg-muted/40" />
      </div>
    </div>
  );
}
