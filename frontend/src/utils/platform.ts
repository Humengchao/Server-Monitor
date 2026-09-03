/**
 * Modifier class for a platform badge: the badges are tinted per OS, and every
 * view derived the same ternary independently.
 *
 * Lives apart from PlatformIcon so that file exports nothing but a component —
 * react-refresh cannot hot-reload a module that mixes the two.
 */
export function platformClass(serverType?: string): 'windows' | 'linux' {
  return serverType === 'windows' ? 'windows' : 'linux';
}
