import axios from 'axios'

import { downloadsHost } from './Hosts'

export const fetchDownloadJobs = async () => {
  const { data } = await axios.get(downloadsHost())
  return data || []
}

export const createDownloadJob = payload => axios.post(downloadsHost(), payload)

export const pauseDownloadJob = id => axios.post(`${downloadsHost()}/${id}/pause`)

export const resumeDownloadJob = id => axios.post(`${downloadsHost()}/${id}/resume`)

export const stopDownloadJob = id => axios.post(`${downloadsHost()}/${id}/stop`)

export const deleteDownloadJob = (id, { removeFiles = false } = {}) => {
  const suffix = removeFiles ? '?remove_files=true' : ''
  return axios.delete(`${downloadsHost()}/${id}${suffix}`)
}
