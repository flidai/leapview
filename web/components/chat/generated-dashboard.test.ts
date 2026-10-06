import {expect,test} from 'bun:test'
import type {ChatTranscriptItemSignal} from '../../generated/signals'
import {generatedDashboardHref} from './generated-dashboard'
const tool=(name:string,extra:Partial<ChatTranscriptItemSignal>={}):ChatTranscriptItemSignal=>({id:name,kind:'tool',name,runId:'run-new',toolCallId:name,status:'complete',...extra})
test('completed new dashboard opens the exact retained preview within the current chat',()=>{
 const href=generatedDashboardHref([tool('create_dashboard_draft'),tool('edit_dashboard_source'),tool('preview_dashboard_draft')],'run-new','conversation')
 expect(href).toBe('/chats/conversation/actions/preview_dashboard_draft/open?run=run-new&embed=chat&createdBy=create_dashboard_draft')
})
test('history, individual visuals, unfinished drafts and failed previews do not auto-open',()=>{
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft')],'other-run','conversation')).toBe('')
 expect(generatedDashboardHref([tool('add_dashboard_visual'),tool('preview_dashboard_draft')],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft')],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft',{resultJson:'{"visualErrors":{"chart":"Missing measure"}}'})],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft',{status:'error',error:'Invalid query'})],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft',{resultJson:'{"error":"invalid"}'})],'run-new','conversation')).toBe('')
})
test('a preview from before the new draft was created must not open that draft',()=>{
 expect(generatedDashboardHref([tool('preview_dashboard_draft'),tool('create_dashboard_draft')],'run-new','conversation')).toBe('')
})
test('a recovery run opens the newly completed dashboard after creation in an earlier run',()=>{
 const created=tool('create_dashboard_draft',{runId:'interrupted',resultJson:'{"lifecycle":{"id":"dashboard-a"}}'})
 const edit=tool('edit_dashboard_source',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const preview=tool('preview_dashboard_draft',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const href=generatedDashboardHref([created,edit,preview],'run-new','conversation')
 expect(href).toContain('/actions/preview_dashboard_draft/open')
 expect(href).not.toContain('mode=preview')
 expect(generatedDashboardHref([created,preview],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([created,{...edit,argumentsJson:'{"dashboardId":"other"}'},preview],'run-new','conversation')).toBe('')
})

test('an edit after preview requires a fresh preview before opening',()=>{
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft'),tool('edit_dashboard_source')],'run-new','conversation')).toBe('')
})
