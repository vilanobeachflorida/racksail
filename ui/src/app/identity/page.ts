/**
 * Full page loads, for when everything the page holds in memory belongs to
 * someone else after a change, as after signing out or viewing the panel as
 * another user. Tests watch them here.
 */
export const page = {
  /** Loads a path afresh. */
  load(path: string): void {
    window.location.assign(path);
  },
};
