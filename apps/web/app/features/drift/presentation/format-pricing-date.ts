import { formatDate } from './presentation-utils';

export function formatPricingDate(value: string) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return formatDate(value);
  return `${Number(match[1])}年${Number(match[2])}月${Number(match[3])}日`;
}
