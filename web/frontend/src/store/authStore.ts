import { create } from 'zustand';

interface AuthState {
  token: string | null;
  expired: boolean;
  expire: () => void;
  user: { id: string; username: string } | null;
  setAuth: (token: string, user: { id: string; username: string }) => void;
  logout: () => void;
  isLoggedIn: () => boolean;
}

// Corrupted localStorage must not crash the whole app at module load time.
function readStoredUser(): AuthState['user'] {
  try {
    return JSON.parse(localStorage.getItem('user') || 'null');
  } catch {
    return null;
  }
}

export const useAuthStore = create<AuthState>((set, get) => ({
  token: localStorage.getItem('token'),
  expired: false,
  expire: () => { localStorage.removeItem('token'); localStorage.removeItem('user'); set({ expired: true }); },
  user: readStoredUser(),
  setAuth: (token, user) => {
    localStorage.setItem('token', token);
    localStorage.setItem('user', JSON.stringify(user));
    set({ token, user, expired: false });
  },
  logout: () => {
    localStorage.removeItem('token');
    localStorage.removeItem('user');
    set({ token: null, user: null, expired: false });
  },
  isLoggedIn: () => !!get().token,
}));
