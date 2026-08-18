/** Exact, fixed-point Decimal transport primitives shared by visual adapters. */
export type DecimalParts = { negative: boolean; integer: string; fraction: string }

export function parseDecimal(value: string): DecimalParts | undefined {
  const match = /^(-?)(0|[1-9]\d*)(?:\.(\d+))?$/.exec(value)
  if (!match) return undefined
  const negative = match[1] === '-'
  const integer = match[2]!
  const fraction = match[3] ?? ''
  if (negative && integer === '0' && /^0*$/.test(fraction)) return undefined
  return { negative, integer, fraction }
}

export function roundDecimal(value: DecimalParts, maximum: number): DecimalParts {
  let digits = value.integer + value.fraction
  const keep = value.integer.length + maximum
  if (digits.length < keep) digits += '0'.repeat(keep - digits.length)
  let kept = digits.slice(0, keep)
  const discarded = digits.slice(keep)
  if (discarded.length > 0 && discarded[0]! >= '5') kept = (BigInt(kept || '0') + 1n).toString()
  const integerWidth = Math.max(1, kept.length - maximum)
  if (kept.length < integerWidth + maximum) kept = '0'.repeat(integerWidth + maximum - kept.length) + kept
  const roundedDigits = BigInt(kept || '0')
  return { negative: value.negative && roundedDigits !== 0n, integer: kept.slice(0, integerWidth) || '0', fraction: kept.slice(integerWidth).padEnd(maximum, '0') }
}

export function decimalShift(value: string, power: number): string {
  const parsed = parseDecimal(value)
  if (!parsed || !Number.isInteger(power)) throw new Error('visualization value must be a canonical decimal string')
  let digits = parsed.integer + parsed.fraction
  let point = parsed.integer.length + power
  if (point <= 0) {
    digits = '0'.repeat(1 - point) + digits
    point = 1
  }
  if (point >= digits.length) digits += '0'.repeat(point - digits.length)
  const integer = digits.slice(0, point).replace(/^0+(?=\d)/, '') || '0'
  const fraction = digits.slice(point).replace(/0+$/, '')
  return (parsed.negative ? '-' : '') + integer + (fraction ? '.' + fraction : '')
}

export function decimalMagnitudeOrder(value: string): number {
  const parsed = parseDecimal(value)
  if (!parsed) throw new Error('visualization value must be a canonical decimal string')
  if (parsed.integer !== '0') return parsed.integer.length - 1
  const first = parsed.fraction.search(/[1-9]/)
  return first < 0 ? -Infinity : -(first + 1)
}

export function decimalSignedInteger(value: string | number): { signedDigits: bigint; digits: bigint; scale: number } {
  const parsed = typeof value === 'string' ? parseDecimal(value) : parseNumber(value)
  if (!parsed) return { signedDigits: 0n, digits: 0n, scale: 0 }
  const digits = BigInt(parsed.integer + parsed.fraction || '0')
  return { signedDigits: (parsed.negative ? -1n : 1n) * digits, digits, scale: parsed.fraction.length }
}

export function decimalToString(negative: boolean, digits: bigint, scale: number): string {
  const absolute = digits < 0n ? -digits : digits
  if (absolute === 0n) return '0'
  const raw = absolute.toString().padStart(scale + 1, '0')
  const point = raw.length - scale
  const integer = raw.slice(0, point) || '0'
  const fraction = raw.slice(point).replace(/0+$/, '')
  return `${negative ? '-' : ''}${integer}${fraction ? `.${fraction}` : ''}`
}

export function decimalFraction(
  numerator: bigint,
  numeratorScale: bigint,
  denominator: bigint,
  denominatorScale: bigint,
  maximumScale = 18,
): string {
  if (denominator === 0n) return '0'
  const signed = (numerator < 0n) !== (denominator < 0n)
  const absoluteNumerator = numerator < 0n ? -numerator : numerator
  const absoluteDenominator = denominator < 0n ? -denominator : denominator
  const scaledNumerator = absoluteNumerator * denominatorScale * 10n ** BigInt(maximumScale)
  const scaledDenominator = absoluteDenominator * numeratorScale
  let quotient = scaledNumerator / scaledDenominator
  const remainder = scaledNumerator % scaledDenominator
  const twice = remainder * 2n
  if (twice > scaledDenominator || twice === scaledDenominator && quotient % 2n === 1n) quotient += 1n
  return decimalToString(signed && quotient !== 0n, quotient, maximumScale)
}

function parseNumber(value: number): DecimalParts | undefined {
  if (!Number.isFinite(value)) return undefined
  const raw = String(value)
  return parseDecimal(raw.includes('e') ? value.toFixed(12).replace(/0+$/, '').replace(/\.$/, '') : raw)
}
