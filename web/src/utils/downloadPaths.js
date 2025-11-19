const normalizeCategory = value => (value || '').toString().trim().toLowerCase()

const CATEGORY_MAP = new Map([
  ['movie', 'movie'],
  ['movies', 'movie'],
  ['film', 'movie'],
  ['films', 'movie'],
  ['фильм', 'movie'],
  ['фильмы', 'movie'],
  ['series', 'series'],
  ['tv', 'series'],
  ['tvshow', 'series'],
  ['serial', 'series'],
  ['сериал', 'series'],
  ['сериалы', 'series'],
  ['music', 'music'],
  ['музыка', 'music'],
  ['other', 'other'],
  ['misc', 'other'],
  ['разное', 'other'],
  ['без категории', 'uncategorized'],
  ['uncategorized', 'uncategorized'],
  ['none', 'uncategorized'],
])

const ensureCategoryFolder = category => {
  const normalized = normalizeCategory(category)
  if (!normalized) {
    return 'uncategorized'
  }
  return CATEGORY_MAP.get(normalized) || 'other'
}

const joinPathSegment = (basePath, segment) => {
  if (!segment) {
    return basePath || ''
  }
  if (!basePath) {
    return segment
  }
  const separator = basePath.includes('\\') && !basePath.includes('/') ? '\\' : '/'
  const trimmedBase = basePath.replace(/[\\/]+$/, '')
  return `${trimmedBase}${separator}${segment}`
}

export const getDefaultDownloadPath = (basePath, category) => {
  const normalizedBase = (basePath || '').trim()
  if (!normalizedBase) {
    return ''
  }
  const folder = ensureCategoryFolder(category)
  return joinPathSegment(normalizedBase, folder)
}

export default getDefaultDownloadPath
