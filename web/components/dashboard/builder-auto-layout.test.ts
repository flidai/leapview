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
