import axios from 'axios'
import { memo, useState } from 'react'
import { playlistTorrHost, torrentsHost, viewedHost } from 'utils/Hosts'
import { CopyToClipboard } from 'react-copy-to-clipboard'
import { Button } from '@material-ui/core'
import ptt from 'parse-torrent-title'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from 'react-query'
import TorrentDownloadControls from 'components/TorrentDownloadControls'

import { SmallLabel, MainSectionButtonGroup } from './style'
import { SectionSubName } from '../style'
import DownloadDialog from './DownloadDialog'

const TorrentFunctions = memo(
  ({
    hash,
    viewedFileList,
    playableFileList,
    name,
    title,
    category,
    poster,
    data,
    setViewedFileList,
    fileStats,
    downloadPath,
    downloads = [],
  }) => {
    const { t } = useTranslation()
    const [isDownloadDialogOpen, setIsDownloadDialogOpen] = useState(false)
    const queryClient = useQueryClient()
    const latestViewedFileId = viewedFileList?.[viewedFileList?.length - 1]
    const latestViewedFile = playableFileList?.find(({ id }) => id === latestViewedFileId)?.path
    const isOnlyOnePlayableFile = playableFileList?.length === 1
    const latestViewedFileData = latestViewedFile && ptt.parse(latestViewedFile)
    const downloadsList = downloads || []
    const dropTorrent = () => axios.post(torrentsHost(), { action: 'drop', hash })
    const removeTorrentViews = () =>
      axios.post(viewedHost(), { action: 'rem', hash, file_index: -1 }).then(() => setViewedFileList())
    const fullPlaylistLink = `${playlistTorrHost()}/${encodeURIComponent(name || title || 'file')}.m3u?link=${hash}&m3u`
    const partialPlaylistLink = `${fullPlaylistLink}&fromlast`
    const magnet = `magnet:?xt=urn:btih:${hash}&dn=${encodeURIComponent(name || title)}`
    const refreshTorrents = () => queryClient.invalidateQueries('torrents')

    return (
      <>
        {!isOnlyOnePlayableFile && !!viewedFileList?.length && (
          <>
            <SmallLabel>{t('DownloadPlaylist')}</SmallLabel>
            <SectionSubName mb={10}>
              {t('LatestFilePlayed')}{' '}
              <strong>
                {latestViewedFileData?.title}.
                {latestViewedFileData?.season && (
                  <>
                    {' '}
                    {t('Season')}: {latestViewedFileData?.season}. {t('Episode')}: {latestViewedFileData?.episode}.
                  </>
                )}
              </strong>
            </SectionSubName>

            <MainSectionButtonGroup>
              <a style={{ textDecoration: 'none' }} href={fullPlaylistLink}>
                <Button style={{ width: '100%' }} variant='contained' color='primary' size='large'>
                  {t('Full')}
                </Button>
              </a>

              <a style={{ textDecoration: 'none' }} href={partialPlaylistLink}>
                <Button style={{ width: '100%' }} variant='contained' color='primary' size='large'>
                  {t('FromLatestFile')}
                </Button>
              </a>
            </MainSectionButtonGroup>
          </>
        )}
        <SmallLabel mb={10}>{t('TorrentState')}</SmallLabel>
        <MainSectionButtonGroup>
          <Button onClick={() => removeTorrentViews()} variant='contained' color='primary' size='large'>
            {t('RemoveViews')}
          </Button>
          <Button onClick={() => dropTorrent()} variant='contained' color='primary' size='large'>
            {t('DropTorrent')}
          </Button>
        </MainSectionButtonGroup>
        <SmallLabel mb={10}>{t('Downloads.SectionTitle')}</SmallLabel>
        {downloadsList.length ? (
          <TorrentDownloadControls downloads={downloadsList} variant='details' onActionComplete={refreshTorrents} />
        ) : (
          <SectionSubName mb={10}>{t('Downloads.Empty')}</SectionSubName>
        )}
        <MainSectionButtonGroup>
          <Button onClick={() => setIsDownloadDialogOpen(true)} variant='contained' color='primary' size='large'>
            {t('Downloads.OpenDialogButton')}
          </Button>
        </MainSectionButtonGroup>
        <SmallLabel mb={10}>{t('Info')}</SmallLabel>
        <MainSectionButtonGroup>
          {(isOnlyOnePlayableFile || !viewedFileList?.length) && (
            <a style={{ textDecoration: 'none' }} href={fullPlaylistLink}>
              <Button style={{ width: '100%' }} variant='contained' color='primary' size='large'>
                {t('DownloadPlaylist')}
              </Button>
            </a>
          )}
          <CopyToClipboard text={magnet}>
            <Button variant='contained' color='primary' size='large'>
              {t('CopyHash')}
            </Button>
          </CopyToClipboard>
        </MainSectionButtonGroup>
        {isDownloadDialogOpen && (
          <DownloadDialog
            open={isDownloadDialogOpen}
            handleClose={() => setIsDownloadDialogOpen(false)}
            torrent={{ hash, title, name, category, poster, data }}
            fileStats={fileStats}
            defaultPath={downloadPath}
          />
        )}
      </>
    )
  },
)

export default TorrentFunctions
