import styled from 'styled-components'

export const DownloadStatusList = styled.div`
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-bottom: 20px;
`

export const DownloadStatusRow = styled.div`
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 10px;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;

  .MuiLinearProgress-root {
    height: 8px;
    border-radius: 4px;
  }
`

export const DownloadStatusHeader = styled.div`
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: center;

  .title {
    font-size: 16px;
    font-weight: 600;
    margin-bottom: 4px;
  }

  small {
    font-size: 12px;
    color: rgba(255, 255, 255, 0.7);
  }
`

export const DownloadStatusMeta = styled.div`
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: rgba(255, 255, 255, 0.8);

  @media (max-width: 600px) {
    flex-direction: column;
    gap: 4px;
  }
`

export const DownloadStatusActions = styled.div`
  display: flex;
  justify-content: flex-end;
`

export const DownloadStatusError = styled.div`
  font-size: 12px;
  color: #ff9f43;
  word-break: break-word;
`

export const StatusChip = styled.span`
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  color: #fff;
  text-transform: capitalize;
`
