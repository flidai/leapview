@apigen.`package`(#{
  title: "APIGen Signal-Like Data Contracts",
  version: "1.0.0",
  description: "Small data-contract example shaped like a dashboard signal envelope.",
})
namespace SignalContracts;

model DashboardPageSignal {
  dashboardId: string;
  pageId: string;
  title: string;
}

model DashboardVisual {
  id: string;
  kind: string;
  title: string;
  data: Record<unknown>;
}

model DashboardTable {
  id: string;
  columns: string[];
  rows: Record<unknown>[];
}

model DashboardFilterControl {
  type: string;
  value?: string;
  values?: string[];
}

model DashboardFilters {
  controls: Record<DashboardFilterControl>;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "datastar"] })
@apigen.`metadata`(#{ `x-libredash-surface`: "dashboard" })
model DashboardEnvelope {
  @apigen.`metadata`(#{ `x-libredash-signal-key`: "page" })
  page: DashboardPageSignal;

  @apigen.`metadata`(#{ `x-libredash-signal-key`: "filters", `x-libredash-patch-mode`: "replace" })
  filters: DashboardFilters;

  @apigen.`metadata`(#{ `x-libredash-signal-key`: "visuals", `x-libredash-patch-mode`: "merge" })
  visuals: Record<DashboardVisual>;

  @apigen.`metadata`(#{ `x-libredash-signal-key`: "tables", `x-libredash-patch-mode`: "merge" })
  tables: Record<DashboardTable>;
}
