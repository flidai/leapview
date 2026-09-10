import { init, use } from 'echarts/core'
import type {
  VisualizationCartesianMark,
  VisualizationHierarchyMark,
  VisualizationPolarMark,
  VisualizationProportionalMark,
} from '../../../../generated/visualization'
import {
  BarChart,
  BoxplotChart,
  CandlestickChart,
  FunnelChart,
  GaugeChart,
  GraphChart,
  HeatmapChart,
  LineChart,
  PieChart,
  RadarChart,
  SankeyChart,
  ScatterChart,
  SunburstChart,
  TreeChart,
  TreemapChart,
} from 'echarts/charts'
import {
  AriaComponent,
  AxisPointerComponent,
  BrushComponent,
  DataZoomComponent,
  DatasetComponent,
  GraphicComponent,
  GridComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  PolarComponent,
  RadarComponent,
  TitleComponent,
  ToolboxComponent,
  TooltipComponent,
  TransformComponent,
  VisualMapComponent,
} from 'echarts/components'
import { LabelLayout, LegacyGridContainLabel, UniversalTransition } from 'echarts/features'
import { CanvasRenderer } from 'echarts/renderers'

type EChartsMark = VisualizationCartesianMark | VisualizationHierarchyMark | VisualizationPolarMark | VisualizationProportionalMark

// This mapping is exhaustive over the generated visualization contract, so a
// newly supported mark cannot silently ship without its ECharts implementation.
const chartsByMark = {
  line: LineChart,
  area: LineChart,
  bar: BarChart,
  column: BarChart,
  histogram: BarChart,
  combo: LineChart,
  waterfall: BarChart,
  candlestick: CandlestickChart,
  boxplot: BoxplotChart,
  heatmap: HeatmapChart,
  treemap: TreemapChart,
  sunburst: SunburstChart,
  tree: TreeChart,
  sankey: SankeyChart,
  graph: GraphChart,
  radar: RadarChart,
  gauge: GaugeChart,
  pie: PieChart,
  donut: PieChart,
  funnel: FunnelChart,
} satisfies Record<EChartsMark, typeof BarChart>

use([
  ...new Set(Object.values(chartsByMark)),
  // Point visualizations have no mark discriminator and always use scatter.
  ScatterChart,
  AriaComponent,
  AxisPointerComponent,
  BrushComponent,
  DataZoomComponent,
  DatasetComponent,
  GraphicComponent,
  GridComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  PolarComponent,
  RadarComponent,
  TitleComponent,
  ToolboxComponent,
  TooltipComponent,
  TransformComponent,
  VisualMapComponent,
  LabelLayout,
  LegacyGridContainLabel,
  UniversalTransition,
  CanvasRenderer,
])

export { init }
