import { memo, useCallback, useMemo, useState } from 'react'
import { IconButton, LinearProgress, Tooltip } from '@material-ui/core'
import PauseIcon from '@material-ui/icons/Pause'
import PlayArrowIcon from '@material-ui/icons/PlayArrow'
import DeleteSweepIcon from '@material-ui/icons/DeleteSweep'
import DeleteOutlineIcon from '@material-ui/icons/DeleteOutline'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from 'react-query'
import { humanizeSize, humanizeSpeed } from 'utils/Utils'
import { deleteDownloadJob, pauseDownloadJob, resumeDownloadJob } from 'utils/downloads'
import { SmallStyledButton } from 'components/TorrentCard/style'
import DownloadRemoveDialog from 'components/DownloadRemoveDialog'

import {
  DownloadStatusActions,
  DownloadStatusError,
  DownloadStatusHeader,
  DownloadStatusList,
  DownloadStatusMeta,
  DownloadStatusRow,
  StatusChip,
} from './style'

const statusPalette = {
  pending: '#ffa000',
  running: '#0288d1',
  paused: '#7b1fa2',
  completed: '#2e7d32',
  failed: '#c62828',
  canceled: '#546e7a',
}

const activeStatuses = ['pending', 'running', 'paused']

const pickJobField = (job, ...keys) => {
  if (!job) return undefined
  for (const key of keys) {
    if (job[key] !== undefined && job[key] !== null) {
      return job[key]
    }
  }
  return undefined
}

const normalizeTimestamp = value => {
  if (!value) return null
  if (value instanceof Date) {
    return value.getTime()
  }
  if (typeof value === 'number') {
    return value > 1e12 ? value : value * 1000
  }
  if (typeof value === 'string') {
    const numeric = Number(value)
    if (!Number.isNaN(numeric) && value.trim() === `${numeric}`) {
      return numeric > 1e12 ? numeric : numeric * 1000
    }
    const date = new Date(value)
    if (!Number.isNaN(date.getTime())) {
      return date.getTime()
    }
    return value
  }
  return null
}

const normalizeJob = job => {
  const status = (pickJobField(job, 'Status', 'status') || '').toLowerCase()
  return {
    id: pickJobField(job, 'ID', 'id'),
    status,
    title: pickJobField(job, 'title', 'Title'),
    hash: pickJobField(job, 'hash', 'Hash'),
    targetPath: pickJobField(job, 'target_path', 'TargetPath'),
    error: pickJobField(job, 'Error', 'error'),
    updatedAt: normalizeTimestamp(pickJobField(job, 'updated_at', 'UpdatedAt', 'created_at', 'CreatedAt')),
    bytesCompleted: pickJobField(job, 'bytes_completed', 'BytesCompleted') || 0,
    bytesTotal: pickJobField(job, 'bytes_total', 'BytesTotal') || 0,
    raw: job,
  }
}

const TorrentDownloadControls = memo(({ downloads = [], variant = 'card', downloadSpeed = 0, onActionComplete }) => {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [activeJobId, setActiveJobId] = useState(null)
  const [deleteDialogJob, setDeleteDialogJob] = useState(null)
  const [deletePending, setDeletePending] = useState(false)

  const normalizedDownloads = useMemo(() => downloads.map(normalizeJob), [downloads])
  const activeDownload = useMemo(() => {
    if (!normalizedDownloads.length) return null
    return normalizedDownloads.find(job => activeStatuses.includes(job.status)) || normalizedDownloads[0]
  }, [normalizedDownloads])

  const refreshData = useCallback(() => {
    queryClient.invalidateQueries('torrents')
    if (typeof onActionComplete === 'function') {
      onActionComplete()
    }
  }, [onActionComplete, queryClient])

  const runAction = useCallback(
    async (jobId, action) => {
      if (!jobId || typeof action !== 'function') return
      setActiveJobId(jobId)
      try {
        await action(jobId)
        refreshData()
      } catch (err) {
        // eslint-disable-next-line no-console
        console.error(err)
      } finally {
        setActiveJobId(null)
      }
    },
    [refreshData],
  )

  const handlePause = useCallback(jobId => runAction(jobId, pauseDownloadJob), [runAction])
  const handleResume = useCallback(jobId => runAction(jobId, resumeDownloadJob), [runAction])

  const handleDeleteConfirm = useCallback(
    async removeFiles => {
      if (!deleteDialogJob) return
      setDeletePending(true)
      try {
        await runAction(deleteDialogJob.id, id => deleteDownloadJob(id, { removeFiles }))
      } finally {
        setDeletePending(false)
        setDeleteDialogJob(null)
      }
    },
    [deleteDialogJob, runAction],
  )

  const handleDeleteClose = useCallback(() => {
    if (deletePending) return
    setDeleteDialogJob(null)
  }, [deletePending])

  const renderActionButtons = job => {
    if (!job) return null
    const { id, status } = job
    const canPause = ['pending', 'running'].includes(status)
    const canResume = status === 'paused'
    const disabled = activeJobId === id || (deletePending && deleteDialogJob?.id === id)

    if (variant === 'card') {
      return (
        <div className='download-status__actions'>
          {canPause && (
            <SmallStyledButton disabled={disabled} onClick={() => handlePause(id)}>
              <PauseIcon />
              <span>{t('Downloads.Pause')}</span>
            </SmallStyledButton>
          )}
          {canResume && (
            <SmallStyledButton disabled={disabled} onClick={() => handleResume(id)}>
              <PlayArrowIcon />
              <span>{t('Downloads.Resume')}</span>
            </SmallStyledButton>
          )}
          <SmallStyledButton disabled={disabled} onClick={() => setDeleteDialogJob(job)}>
            <DeleteSweepIcon />
            <span>{t('Downloads.Delete')}</span>
          </SmallStyledButton>
        </div>
      )
    }

    return (
      <DownloadStatusActions>
        {canPause && (
          <Tooltip title={t('Downloads.Pause')}>
            <span>
              <IconButton color='secondary' disabled={disabled} onClick={() => handlePause(id)}>
                <PauseIcon />
              </IconButton>
            </span>
          </Tooltip>
        )}
        {canResume && (
          <Tooltip title={t('Downloads.Resume')}>
            <span>
              <IconButton color='secondary' disabled={disabled} onClick={() => handleResume(id)}>
                <PlayArrowIcon />
              </IconButton>
            </span>
          </Tooltip>
        )}
        <Tooltip title={t('Downloads.Delete')}>
          <span>
            <IconButton color='secondary' disabled={disabled} onClick={() => setDeleteDialogJob(job)}>
              <DeleteOutlineIcon />
            </IconButton>
          </span>
        </Tooltip>
      </DownloadStatusActions>
    )
  }

  if (!normalizedDownloads.length) {
    return null
  }

  const dialog = (
    <DownloadRemoveDialog
      open={!!deleteDialogJob}
      jobTitle={deleteDialogJob?.title || deleteDialogJob?.hash}
      pending={deletePending}
      onConfirm={handleDeleteConfirm}
      onClose={handleDeleteClose}
    />
  )

  if (variant === 'details') {
    return (
      <>
        <DownloadStatusList>
          {normalizedDownloads.map(job => {
            const progress = job.bytesTotal ? Math.min(100, Math.round((job.bytesCompleted / job.bytesTotal) * 100)) : 0
            const sizeLabel = job.bytesTotal
              ? `${humanizeSize(job.bytesCompleted)} / ${humanizeSize(job.bytesTotal)}`
              : humanizeSize(job.bytesCompleted) || `0 ${t('B')}`
            const statusLabel = job.status ? t(`Downloads.Status.${job.status}`) : t('Downloads.Status.pending')
            const updatedLabel = job.updatedAt ? new Date(job.updatedAt).toLocaleString() : '—'
            return (
              <DownloadStatusRow key={job.id || job.hash}>
                <DownloadStatusHeader>
                  <div>
                    <div className='title'>{job.title || job.hash}</div>
                    <small>{job.targetPath}</small>
                  </div>
                  <StatusChip color={statusPalette[job.status] || statusPalette.pending}>{statusLabel}</StatusChip>
                </DownloadStatusHeader>

                <LinearProgress variant={job.bytesTotal ? 'determinate' : 'indeterminate'} value={progress} />

                <DownloadStatusMeta>
                  <span>{sizeLabel}</span>
                  <span>{updatedLabel}</span>
                </DownloadStatusMeta>

                {!!job.error && <DownloadStatusError>{job.error}</DownloadStatusError>}

                {renderActionButtons(job)}
              </DownloadStatusRow>
            )
          })}
        </DownloadStatusList>
        {dialog}
      </>
    )
  }

  const statusKey = activeDownload?.status || 'pending'
  const statusLabel = statusKey ? t(`Downloads.Status.${statusKey}`) : t('Downloads.Status.pending')
  const chipColor = statusPalette[statusKey] || statusPalette.pending
  const progress = activeDownload?.bytesTotal
    ? Math.min(100, Math.round((activeDownload.bytesCompleted / activeDownload.bytesTotal) * 100))
    : 0
  const completedLabel = humanizeSize(activeDownload?.bytesCompleted) || `0 ${t('B')}`
  const totalLabel = activeDownload?.bytesTotal ? humanizeSize(activeDownload.bytesTotal) : '---'

  return (
    <>
      <div className='download-status'>
        <div className='download-status__header'>
          <span>{t('Downloads.SectionTitle')}</span>
          <span className='download-status__chip' style={{ backgroundColor: chipColor }}>
            {statusLabel}
          </span>
        </div>
        <LinearProgress variant={activeDownload?.bytesTotal ? 'determinate' : 'indeterminate'} value={progress} />
        <div className='download-status__meta'>
          <span>
            {completedLabel}
            {activeDownload?.bytesTotal ? ` / ${totalLabel}` : ''}
          </span>
          {statusKey === 'running' && <span>{`${progress}%`}</span>}
          <div>{downloadSpeed > 0 ? humanizeSpeed(downloadSpeed) : '---'}</div>
        </div>
        {!!activeDownload?.error && <div className='download-status__error'>{activeDownload.error}</div>}
        {renderActionButtons(activeDownload)}
      </div>
      {dialog}
    </>
  )
})

export default TorrentDownloadControls
