import {expect,test} from 'bun:test'
import {echartsOption,responsiveEChartsPatch} from './echarts'
import {proportionalFixture} from './echarts-test-fixtures'
for(const mark of ['pie','donut'] as const)test(`${mark} bottom legend remains centered in wide and compact cards`,()=>{
 const e=proportionalFixture(mark)
 if(e.spec.kind!=='proportional')throw Error('fixture')
 e.spec.presentation.legend='bottom'
 const option=echartsOption(e) as any
 expect(option.legend).toMatchObject({bottom:0,left:'center',right:'auto',orient:'horizontal'})
 for(const width of [400,900])expect((responsiveEChartsPatch(option,width,400) as any).legend).toMatchObject({bottom:0,left:'center',right:'auto'})
})
