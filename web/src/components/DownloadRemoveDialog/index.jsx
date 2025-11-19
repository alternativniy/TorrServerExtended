import { useEffect, useState } from 'react'
import {
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
} from '@material-ui/core'
import { useTranslation } from 'react-i18next'

const DownloadRemoveDialog = ({ open, jobTitle, pending, onConfirm, onClose }) => {
  const { t } = useTranslation()
  const [removeFiles, setRemoveFiles] = useState(false)

  useEffect(() => {
    if (!open) {
      setRemoveFiles(false)
    }
  }, [open])

  const handleConfirm = () => {
    if (typeof onConfirm === 'function') {
      onConfirm(removeFiles)
    }
  }

  return (
    <Dialog open={open} onClose={pending ? undefined : onClose} maxWidth='xs' fullWidth>
      <DialogTitle>{t('Downloads.DeleteDialog.Title')}</DialogTitle>
      <DialogContent>
        <DialogContentText>
          {jobTitle
            ? t('Downloads.DeleteDialog.DescriptionWithTitle', { title: jobTitle })
            : t('Downloads.DeleteDialog.Description')}
        </DialogContentText>
        <FormControlLabel
          control={
            <Checkbox
              color='primary'
              checked={removeFiles}
              onChange={(_, checked) => setRemoveFiles(checked)}
              disabled={pending}
            />
          }
          label={t('Downloads.DeleteDialog.RemoveFiles')}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} color='primary' disabled={pending}>
          {t('Cancel')}
        </Button>
        <Button onClick={handleConfirm} color='secondary' variant='contained' disabled={pending}>
          {t('Downloads.DeleteDialog.Confirm')}
        </Button>
      </DialogActions>
    </Dialog>
  )
}

export default DownloadRemoveDialog
