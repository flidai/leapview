type GeneratedEnvelopeValidator = typeof import('../../../generated/visualization/validate')['default']
export let generatedEnvelopeValidator: GeneratedEnvelopeValidator | undefined
let envelopeValidatorLoad: Promise<GeneratedEnvelopeValidator> | undefined

export function loadEnvelopeValidator(): Promise<GeneratedEnvelopeValidator> {
  if (generatedEnvelopeValidator) return Promise.resolve(generatedEnvelopeValidator)
  return envelopeValidatorLoad ??= import('../../../generated/visualization/validate').then(({ default: validate }) => {
    generatedEnvelopeValidator = validate
    return validate
  }, (error: unknown) => {
    envelopeValidatorLoad = undefined
    throw error
  })
}

export class VisualPreparationTimeout extends Error {
  constructor() { super('Loading this visual took too long. Try again.') }
}

export function prepareVisual<T>(operation: () => Promise<T>): Promise<T> {
  let timeout: ReturnType<typeof setTimeout>
  const deadline = new Promise<never>((_, reject) => {
    timeout = setTimeout(() => reject(new VisualPreparationTimeout()), 30_000)
  })
  return Promise.race([Promise.resolve().then(operation), deadline]).finally(() => clearTimeout(timeout))
}
