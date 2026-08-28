import axios from 'axios';

declare module 'axios' {
  export interface AxiosRequestConfig {
    /**
     * Opt this request out of the global "401 means the session died, sign the
     * user out" rule. For endpoints that validate a credential carried in the
     * request body, a 401 is about that credential, not the session.
     */
    skipAuthRedirect?: boolean;
  }
}

const baseURL = import.meta.env.VITE_API_URL || `${window.location.origin}/api`;

const client = axios.create({
  baseURL,
  timeout: 10000,
  headers: {
    'Cache-Control': 'no-cache, no-store, must-revalidate',
    'Pragma': 'no-cache',
  },
});

client.interceptors.request.use((config) => {
  const token = localStorage.getItem('token');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

client.interceptors.response.use(
  (res) => res,
  (err) => {
    // Endpoints that verify a credential in the request body can answer 401
    // about that credential rather than about the session. Those must not tear
    // down the session — see skipAuthRedirect below.
    const skipRedirect = (err.config as { skipAuthRedirect?: boolean } | undefined)?.skipAuthRedirect;
    if (err.response?.status === 401 && !skipRedirect
      && !['/login', '/register'].includes(window.location.pathname)) {
      localStorage.removeItem('token');
      localStorage.removeItem('user');
      window.location.href = '/login';
    }
    return Promise.reject(err);
  }
);

export default client;
