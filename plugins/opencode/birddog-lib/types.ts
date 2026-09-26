/** Runtime states, matching birddog's own vocabulary so the Go adapter that
 *  reads these records needs no translation table of its own. */
export type SessionState = 'active' | 'idle' | 'waiting_input' | 'running_tool';

/** What is known about one outstanding permission request. */
export interface PendingPermission {
  id: string;
  asked_at: string;

  /** What kind of approval is being asked for: "bash", "edit", and so on. */
  type?: string;

  /**
   * What it is asking to do — the command, or the file being edited.
   *
   * Bounded on purpose: an edit request carries a full diff, and birddog
   * records evidence rather than payloads.
   */
  detail?: string;
}

/** The tool a session is currently running. */
export interface CurrentTool {
  name: string;
  started_at: string;
}

/** One session, as the plugin sees it. */
export interface SessionSnapshot {
  session_id: string;
  slug: string;
  title: string;
  directory: string;
  opencode_version: string;

  state: SessionState;
  pending_permission?: PendingPermission;
  current_tool?: CurrentTool;
  last_activity_at: string;
}

/** A record on disk: a snapshot plus who wrote it. */
export interface RegistryRecord extends SessionSnapshot {
  pid: number;

  /**
   * Which plugin instance wrote this. opencode instantiates the plugin twice
   * in one process, so a pid does not identify the writer — and sweeping by
   * pid makes one instance delete the other's live records.
   */
  instance_id: string;

  plugin_version: string;
  updated_at: string;
}
