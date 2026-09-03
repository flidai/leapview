import { decimalFraction, decimalSignedInteger, parseDecimal } from '../../decimal'

type ReferenceScalar = string | number
export type ReferenceReducer = 'first' | 'last' | 'minimum' | 'maximum' | 'mean' | 'median'

export function reduceReferenceValue(values: ReferenceScalar[], reducer: ReferenceReducer): ReferenceScalar | undefined {
  if (values.length === 0) return undefined
  switch (reducer) {
    case 'first': return values[0]
    case 'last': return values.at(-1)
    case 'minimum': return orderedReferenceValue(values, 'minimum')
    case 'maximum': return orderedReferenceValue(values, 'maximum')
    case 'mean': {
      if (values.every((candidate) => typeof candidate === 'string')) return meanDecimalReference(values as string[])
      const numbers = values.filter((candidate): candidate is number => typeof candidate === 'number' && Number.isFinite(candidate))
      return numbers.length === values.length ? numbers.reduce((sum, candidate) => sum + candidate, 0) / numbers.length : undefined
    }
    case 'median': {
      if (values.every((candidate) => typeof candidate === 'string')) {
        const sorted = [...values as string[]].sort(compareDecimalReference)
        const middle = Math.floor(sorted.length / 2)
        return sorted.length % 2 ? sorted[middle] : meanDecimalReference([sorted[middle - 1]!, sorted[middle]!])
      }
      const numbers = values.filter((candidate): candidate is number => typeof candidate === 'number' && Number.isFinite(candidate)).sort((left, right) => left - right)
      if (numbers.length !== values.length) return undefined
      const middle = Math.floor(numbers.length / 2)
      return numbers.length % 2 ? numbers[middle] : (numbers[middle - 1]! + numbers[middle]!) / 2
    }
  }
}

function orderedReferenceValue(values: ReferenceScalar[], reducer: 'minimum' | 'maximum'): ReferenceScalar | undefined {
  if (values.every((value) => typeof value === 'number')) {
    return reducer === 'minimum' ? Math.min(...values as number[]) : Math.max(...values as number[])
  }
  if (values.every((value) => typeof value === 'string')) {
    const sorted = [...values as string[]].sort(values.every((value) => parseDecimal(value)) ? compareDecimalReference : (left, right) => left.localeCompare(right, 'en'))
    return sorted[reducer === 'minimum' ? 0 : values.length - 1]
  }
  return undefined
}

function compareDecimalReference(left: string, right: string): number {
  const a = decimalSignedInteger(left), b = decimalSignedInteger(right)
  const scale = Math.max(a.scale, b.scale)
  const av = a.signedDigits * 10n ** BigInt(scale - a.scale)
  const bv = b.signedDigits * 10n ** BigInt(scale - b.scale)
  return av < bv ? -1 : av > bv ? 1 : 0
}

function meanDecimalReference(values: string[]): string {
  const decimals = values.map(decimalSignedInteger)
  const scale = Math.max(...decimals.map((value) => value.scale))
  const sum = decimals.reduce((total, value) => total + value.signedDigits * 10n ** BigInt(scale - value.scale), 0n)
  return decimalFraction(sum, 10n ** BigInt(scale), BigInt(values.length), 1n, 18)
}
