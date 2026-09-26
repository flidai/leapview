export function catalogColumns(scope: 'all' | 'favorites' | 'mine') {
  if (scope === 'mine') {
    return [
      { id: 'name', label: 'Dashboard', width: '32%' },
      { id: 'dataModel', label: 'Data model', width: '16%' },
      { id: 'popularity', label: 'Popularity', width: '10%', align: 'center' as const, render: 'popularity' as const },
      { id: 'status', label: 'Status', width: '17%', render: 'quiet-status' as const },
      { id: 'updated', label: 'Updated', width: '9%', render: 'datetime' as const },
      { id: 'lastOpened', label: 'Last opened', width: '11%', render: 'datetime' as const },
      { id: 'actions', label: 'Actions', width: '80px', align: 'center' as const, sortable: false, render: 'actions' as const },
    ]
  }
  return [
    { id: 'name', label: 'Dashboard', width: '36%' },
    { id: 'dataModel', label: 'Data model', width: '15%' },
    { id: 'owner', label: 'Owner', width: '9%', align: 'center' as const, render: 'person-avatar' as const },
    { id: 'popularity', label: 'Popularity', width: '10%', align: 'center' as const, render: 'popularity' as const },
    { id: 'updated', label: 'Updated', width: '11%', render: 'datetime' as const },
    { id: 'lastOpened', label: 'Last opened', width: '14%', render: 'datetime' as const },
    { id: 'actions', label: 'Actions', width: '80px', align: 'center' as const, sortable: false, render: 'actions' as const },
  ]
}
