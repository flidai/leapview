import { expect, test } from 'bun:test'
import { arrangeDashboardVisuals } from './builder-auto-layout'

test('arranges compact KPIs, a full-width combo and paired charts without overlap', () => {
 const types = ['combo', 'bar', 'line', 'kpi', 'kpi', 'kpi', 'kpi']
 const result = arrangeDashboardVisuals(types.map((type, i) => ({ id: String(i), type })), [], { columns: 12, rowHeight: 48, gap: 16 })
 expect(result.slice(0, 4).map(p => p.placement)).toEqual([1,4,7,10].map(column => ({ column, row: 1, columnSpan: 3, rowSpan: 2 })))
 expect(result.slice(4).map(p => p.placement)).toEqual([
 { column: 1, row: 3, columnSpan: 12, rowSpan: 5 },
 { column: 1, row: 8, columnSpan: 6, rowSpan: 5 },
 { column: 7, row: 8, columnSpan: 6, rowSpan: 5 },
 ])
})

test('keeps headers and slicers in place and honors custom grid sizing', () => {
 const result = arrangeDashboardVisuals([{id:'pie',type:'pie'}, {id:'table',type:'table'}], [{col:1,row:1,colSpan:4,rowSpan:12}], {columns:8,rowHeight:24,gap:8})
 expect(result.map(p=>p.placement)).toEqual([
 {column:5,row:1,columnSpan:4,rowSpan:10}, {column:1,row:13,columnSpan:8,rowSpan:12},
 ])
})

test('preserves authored visual positions and sizes while fitting new charts around them', () => {
 const manual = { col: 3, row: 2, colSpan: 5, rowSpan: 7 }
 const result = arrangeDashboardVisuals([
  { id: 'manual', type: 'bar', placement: manual },
  { id: 'new', type: 'pie' },
 ], [{ col: 1, row: 1, colSpan: 12, rowSpan: 1 }], { columns: 12, rowHeight: 48, gap: 16 })
 expect(result.find(p => p.componentId === 'manual')!.placement).toEqual({ column: 3, row: 2, columnSpan: 5, rowSpan: 7 })
 const added = result.find(p => p.componentId === 'new')!.placement
 expect(added).toEqual({ column: 1, row: 9, columnSpan: 12, rowSpan: 5 })
 expect(manual).toEqual({ col: 3, row: 2, colSpan: 5, rowSpan: 7 })
})

test('balances three KPI cards and fills an unpaired chart row', () => {
 const result = arrangeDashboardVisuals(['kpi', 'kpi', 'kpi', 'line', 'pie', 'bar'].map((type, i) => ({ id: String(i), type })), [], { columns: 12, rowHeight: 48, gap: 16 })
 expect(result.slice(0, 3).map(p => p.placement.columnSpan)).toEqual([4, 4, 4])
 for (const row of new Set(result.map(p => p.placement.row))) {
  expect(result.filter(p => p.placement.row === row).reduce((width, p) => width + p.placement.columnSpan, 0)).toBe(12)
 }
})

test('fills odd custom grid widths without expanding into a fixed component', () => {
 const result = arrangeDashboardVisuals([{id:'line',type:'line'}, {id:'pie',type:'pie'}], [], {columns:9,rowHeight:48,gap:16})
 expect(result.map(p => p.placement.columnSpan)).toEqual([4,5])
 const fixed = {col:9,row:1,colSpan:1,rowSpan:20}
 const blocked = arrangeDashboardVisuals([{id:'line',type:'line'}, {id:'pie',type:'pie'}], [fixed], {columns:9,rowHeight:48,gap:16})
 expect(blocked.map(p => p.placement.columnSpan)).toEqual([4,4])
})


test('an odd mix of bar, line and pie charts uses a full-width chart and one paired row', () => {
 const result = arrangeDashboardVisuals(['bar','line','pie'].map(type=>({id:type,type})), [], {columns:12,rowHeight:48,gap:16})
 expect(result.map(p=>p.placement)).toEqual([
  {column:1,row:1,columnSpan:12,rowSpan:5},
  {column:1,row:6,columnSpan:6,rowSpan:5},
  {column:7,row:6,columnSpan:6,rowSpan:5},
 ])
})

test('new KPI cards fill a preserved KPI row without moving or resizing existing cards', () => {
 const manual = [1,5].map(col => ({col,row:1,colSpan:4,rowSpan:2}))
 const result = arrangeDashboardVisuals([
  {id:'first',type:'kpi',placement:manual[0]},
  {id:'second',type:'kpi',placement:manual[1]},
  {id:'new',type:'kpi'},
 ], [], {columns:12,rowHeight:48,gap:16})
 expect(result.map(p=>p.placement)).toEqual([1,5,9].map(column=>({column,row:1,columnSpan:4,rowSpan:2})))
 expect(manual).toEqual([1,5].map(col=>({col,row:1,colSpan:4,rowSpan:2})))
})

test('fills complete bands for the CFO KPI, monthly trend, segment pie and country bar', () => {
 const visuals = ['kpi', 'line', 'pie', 'bar'].map((type, index) => ({ id: String(index), type }))
 const slicers = [{ col: 1, row: 1, colSpan: 12, rowSpan: 2 }]
 const grid = { columns: 12, rowHeight: 48, gap: 16 }
 const result = arrangeDashboardVisuals(visuals, slicers, grid)
 expect(result.map(item => item.placement)).toEqual([
  { column: 1, row: 3, columnSpan: 12, rowSpan: 2 },
  { column: 1, row: 5, columnSpan: 12, rowSpan: 5 },
  { column: 1, row: 10, columnSpan: 6, rowSpan: 5 },
  { column: 7, row: 10, columnSpan: 6, rowSpan: 5 },
 ])
 expect(arrangeDashboardVisuals(visuals, slicers, grid)).toEqual(result)
 expect(arrangeDashboardVisuals(result.map(item => ({
  id: item.componentId,
  type: visuals.find(visual => visual.id === item.componentId)!.type,
  placement: { col: item.placement.column, row: item.placement.row, colSpan: item.placement.columnSpan, rowSpan: item.placement.rowSpan },
 })), slicers, grid)).toEqual(result)
 expect(slicers).toEqual([{ col: 1, row: 1, colSpan: 12, rowSpan: 2 }])
})

test('fills remainder columns in balanced KPI and chart bands on custom grids', () => {
 const result = arrangeDashboardVisuals([
  { id: 'metric-1', type: 'kpi' }, { id: 'metric-2', type: 'kpi' }, { id: 'metric-3', type: 'kpi' },
  { id: 'pie', type: 'pie' }, { id: 'bar', type: 'bar' },
 ], [], { columns: 7, rowHeight: 24, gap: 8 })
 expect(result.map(item => item.placement)).toEqual([
  { column: 1, row: 1, columnSpan: 2, rowSpan: 4 },
  { column: 3, row: 1, columnSpan: 2, rowSpan: 4 },
  { column: 5, row: 1, columnSpan: 3, rowSpan: 4 },
  { column: 1, row: 5, columnSpan: 3, rowSpan: 10 },
  { column: 4, row: 5, columnSpan: 4, rowSpan: 10 },
 ])
})

test('stacks visuals without gaps or overlap on a one-column grid', () => {
 const visuals = ['kpi', 'kpi', 'line', 'pie'].map((type, index) => ({ id: String(index), type }))
 expect(arrangeDashboardVisuals(visuals, [], { columns: 1, rowHeight: 48, gap: 16 }).map(item => item.placement)).toEqual([
  { column: 1, row: 1, columnSpan: 1, rowSpan: 2 },
  { column: 1, row: 3, columnSpan: 1, rowSpan: 2 },
  { column: 1, row: 5, columnSpan: 1, rowSpan: 5 },
  { column: 1, row: 10, columnSpan: 1, rowSpan: 5 },
 ])
})

test('uses matching band heights around a partial obstacle without moving it', () => {
 const obstacle = { col: 1, row: 1, colSpan: 4, rowSpan: 7 }
 const result = arrangeDashboardVisuals([
  { id: 'metric-1', type: 'kpi' }, { id: 'metric-2', type: 'kpi' },
  { id: 'trend', type: 'line' },
 ], [obstacle], { columns: 12, rowHeight: 48, gap: 16 })
 expect(result.map(item => item.placement)).toEqual([
  { column: 5, row: 1, columnSpan: 4, rowSpan: 2 },
  { column: 9, row: 1, columnSpan: 4, rowSpan: 2 },
  { column: 5, row: 3, columnSpan: 8, rowSpan: 5 },
 ])
 expect(obstacle).toEqual({ col: 1, row: 1, colSpan: 4, rowSpan: 7 })
})

test('covers every cell once across even, odd and narrow grids without changing visual identity', () => {
 const combinations = [
  ['kpi', 'line', 'pie', 'bar'],
  ['kpi', 'kpi', 'kpi', 'kpi', 'kpi', 'bar', 'line'],
  ['line', 'pie', 'bar', 'donut', 'area'],
  ['pie', 'table', 'bar', 'map', 'kpi'],
 ]
 for (const columns of [1, 2, 3, 7, 10, 12, 13]) {
  for (const types of combinations) {
   const visuals = types.map((type, index) => ({ id: String(index), type }))
   const result = arrangeDashboardVisuals(visuals, [{ col: 1, row: 1, colSpan: columns, rowSpan: 2 }], { columns, rowHeight: 48, gap: 16 })
   expect(result.map(item => item.componentId).sort()).toEqual(visuals.map(item => item.id).sort())
   const bottom = Math.max(...result.map(item => item.placement.row + item.placement.rowSpan))
   for (let row = 3; row < bottom; row++) {
    for (let column = 1; column <= columns; column++) {
     const covering = result.filter(({ placement }) => column >= placement.column && column < placement.column + placement.columnSpan && row >= placement.row && row < placement.row + placement.rowSpan)
     expect(covering.length).toBe(1)
    }
   }
  }
 }
})

test('uses only two short KPI bands for five metrics on a three-column grid', () => {
 const result = arrangeDashboardVisuals(Array.from({ length: 5 }, (_, index) => ({ id: String(index), type: 'kpi' })), [], { columns: 3, rowHeight: 48, gap: 16 })
 expect(result.map(item => item.placement)).toEqual([
  { column: 1, row: 1, columnSpan: 1, rowSpan: 2 },
  { column: 2, row: 1, columnSpan: 1, rowSpan: 2 },
  { column: 3, row: 1, columnSpan: 1, rowSpan: 2 },
  { column: 1, row: 3, columnSpan: 1, rowSpan: 2 },
  { column: 2, row: 3, columnSpan: 2, rowSpan: 2 },
 ])
})
