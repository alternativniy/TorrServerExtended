import { useEffect, useMemo, useState } from 'react'
import {
  Button,
  Checkbox,
  DialogActions,
  DialogContent,
  List,
  ListItem,
  ListItemIcon,
  ListItemText,
  Typography,
} from '@material-ui/core'
import { useTranslation } from 'react-i18next'
import { StyledDialog, StyledHeader } from 'style/CustomMaterialUiStyles'
import { humanizeSize } from 'utils/Utils'
import { createDownloadJob } from 'utils/downloads'
import { useQueryClient } from 'react-query'

const mapFiles = files =>
  files?.map(file => ({
    id: file.id,
    path: file.path,
    length: file.length,
  })) || []

export default function DownloadDialog({ open, handleClose, torrent, fileStats, defaultPath }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const files = useMemo(() => mapFiles(fileStats), [fileStats])
  const allFileIds = useMemo(() => files.map(file => file.id), [files])

  const [selectedIds, setSelectedIds] = useState(allFileIds)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)

  useEffect(() => {
    setSelectedIds(allFileIds)
  }, [allFileIds])

  const toggleFile = id => {
    setSelectedIds(prev => (prev.includes(id) ? prev.filter(item => item !== id) : [...prev, id]))
  }

  const handleSelectAll = () => setSelectedIds(allFileIds)
  const handleClear = () => setSelectedIds([])

  const handleCreate = async () => {
    setIsSubmitting(true)
    setError('')
    setSuccess(false)
    try {
      await createDownloadJob({
        link: torrent.hash,
        files: selectedIds,
        title: torrent.title || torrent.name,
        poster: torrent.poster,
        category: torrent.category,
        data: torrent.data,
      })
      await queryClient.invalidateQueries('torrents')
      setSuccess(true)
      setTimeout(() => {
        setIsSubmitting(false)
        handleClose()
      }, 600)
    } catch (err) {
      const message = err?.response?.data?.error || err?.message || 'Unknown error'
      setError(message)
      setIsSubmitting(false)
    }
  }

  const totalSelectedSize = useMemo(
    () => files.filter(file => selectedIds.includes(file.id)).reduce((acc, file) => acc + file.length, 0),
    [files, selectedIds],
  )
  const selectedSizeLabel = humanizeSize(totalSelectedSize) || `0 ${t('B')}`

  return (
    <StyledDialog open={open} onClose={handleClose} fullWidth maxWidth='md'>
      <StyledHeader>
        <div>{t('Downloads.CreateTitle')}</div>
      </StyledHeader>

      <DialogContent dividers>
        {error && (
          <Typography component='div' style={{ marginBottom: '16px', color: '#d32f2f' }}>
            {error}
          </Typography>
        )}
        {success && (
          <Typography component='div' style={{ marginBottom: '16px', color: '#2e7d32' }}>
            {t('Downloads.Created')}
          </Typography>
        )}

        <Typography variant='body2' color='textSecondary' style={{ marginBottom: '12px' }}>
          {t('Downloads.TargetHint', { path: defaultPath || t('Downloads.DefaultPathUnknown') })}
        </Typography>

        <div style={{ display: 'flex', gap: '10px', marginBottom: '10px' }}>
          <Button size='small' onClick={handleSelectAll} disabled={!files.length}>
            {t('Downloads.SelectAll')}
          </Button>
          <Button size='small' onClick={handleClear} disabled={!files.length}>
            {t('Downloads.ClearSelection')}
          </Button>
          <Typography variant='body2' color='textSecondary' style={{ marginLeft: 'auto' }}>
            {t('Downloads.SelectedSize', { count: selectedIds.length, size: selectedSizeLabel })}
          </Typography>
        </div>

        <List dense>
          {files.map(file => (
            <ListItem key={file.id} button onClick={() => toggleFile(file.id)}>
              <ListItemIcon>
                <Checkbox edge='start' tabIndex={-1} disableRipple checked={selectedIds.includes(file.id)} />
              </ListItemIcon>
              <ListItemText primary={file.path} secondary={humanizeSize(file.length)} />
            </ListItem>
          ))}
          {!files.length && <Typography variant='body2'>{t('Downloads.NoFiles')}</Typography>}
        </List>
      </DialogContent>

      <DialogActions>
        <Button onClick={handleClose} color='secondary' variant='outlined'>
          {t('Cancel')}
        </Button>
        <Button
          onClick={handleCreate}
          color='secondary'
          variant='contained'
          disabled={isSubmitting || selectedIds.length === 0}
        >
          {t('Downloads.StartDownload')}
        </Button>
      </DialogActions>
    </StyledDialog>
  )
}
