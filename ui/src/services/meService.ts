import { httpClient } from '../utils/httpClient'
import type { GetMeResponse } from '../apis/proto/openpay/v1/me'

const BASE_URL = '/openpay/v1'

export const meService = {
  /** Who the operator is, and which permissions and products they have. */
  async get(): Promise<GetMeResponse> {
    const response = await httpClient.get<GetMeResponse>(`${BASE_URL}/me`)
    return response.data
  },
}
