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
 expect(added).toEqual({ column: 1, row: 9, columnSpan: 6, rowSpan: 5 })
 expect(manual).toEqual({ col: 3, row: 2, colSpan: 5, rowSpan: 7 })
})
