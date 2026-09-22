/** Use the existing CSRF-protected browser logout routes, including full navigation. */
export function submitAuthForm(action: '/auth/logout' | '/auth/logout-all'): void {
  const token = document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
  const form = document.createElement('form')
  form.method = 'POST'
  form.action = action
  form.hidden = true
  if (token) {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = 'gorilla.csrf.Token'
    input.value = token
    form.append(input)
  }
  document.body.append(form)
  form.submit()
}
