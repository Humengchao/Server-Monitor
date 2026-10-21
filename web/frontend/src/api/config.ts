import { apiBaseURL } from '../utils/apiURL';
export { websocketURL } from '../utils/apiURL';

export const API_BASE_URL = apiBaseURL(import.meta.env.VITE_API_URL, window.location.origin);
