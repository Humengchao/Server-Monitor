import { createContext } from 'react';

export const UnsavedChangesContext = createContext<(dirty: boolean) => void>(() => undefined);
