export type SortBy = 'custom' | 'title' | 'artist' | 'duration';
export type QualityPref = 'flac' | '192k' | '132k' | '64k';
export type PlayMode = 'sequence' | 'shuffle' | 'repeat_one';
export type LoginPhase =
  | 'idle'
  | 'generating'
  | 'waiting_scan'
  | 'scanned_confirm'
  | 'confirming'
  | 'logged_in'
  | 'expired'
  | 'error';

export type SwatchColor = 'accent' | 'pink' | 'amber' | 'green' | 'sky' | 'purple';

export interface AccountInfo {
  loggedIn: boolean;
  nickname: string;
  mid: number;
  level: number;
  vip: boolean;
}

export interface LoginState {
  phase: LoginPhase;
  qrUrl: string;
  message: string;
}

export interface Playlist {
  id: string;
  name: string;
  desc: string;
  color: string;
  trackIds: string[];
}

export interface Track {
  id: string;
  dedupeKey: string;
  bvid: string;
  aid: number;
  cid: number;
  page: number;
  totalPages: number;
  title: string;
  artist: string;
  durationMs: number;
  favorite: boolean;
  addedAt: number;
}

export interface PlaybackState {
  trackId: string;
  status: 'idle' | 'loading' | 'playing' | 'paused' | 'stopped' | 'ended' | 'error';
  positionMs: number;
  durationMs: number;
  seekable: boolean;
  volume: number;
  muted: boolean;
  mode: PlayMode;
  qualityLabel: string;
  error: string;
}

export interface TrackDownload {
  status: 'downloading' | 'downloaded' | 'error';
  percent?: number;
  error?: string;
}

export interface ImportPreviewItem {
  dedupeKey: string;
  bvid: string;
  aid: number;
  cid: number;
  page: number;
  totalPages: number;
  title: string;
  artist: string;
  durationMs: number;
  selected: boolean;
  alreadyInLibrary: boolean;
}

export interface ImportPreview {
  status: 'loading' | 'ready';
  input: string;
  sourceTitle: string;
  items: ImportPreviewItem[];
  errors: string[];
}

export interface NoticeBanner {
  kind: 'info' | 'error';
  message: string;
}

export interface MusicState {
  version: number;
  locale: string;
  activeView: string;
  sortBy: SortBy;
  preferredQuality: QualityPref;
  account: AccountInfo;
  login: LoginState;
  playlists: Playlist[];
  tracks: Track[];
  recentIds: string[];
  downloads: Record<string, TrackDownload>;
  queue: string[];
  playback: PlaybackState;
  importPreview: ImportPreview | null;
  notice: NoticeBanner | null;
}

export interface HostTheme {
  background: string;
  surface: string;
  sidebar: string;
  border: string;
  text: string;
  secondary: string;
  accent: string;
  onAccent: string;
  weekend: string;
  warning: string;
  warningBg: string;
  selected: string;
  hover: string;
  isDark: boolean;
}

export interface BridgeReply<T = Record<string, unknown>> {
  requestId: string;
  revision: number;
  value?: T;
}

export interface QuailCrateBridge {
  getRevision(): number;
  getState(): Partial<MusicState> | null;
  getTheme(): Partial<HostTheme> | null;
  getLocale(): string;
  onState(cb: (payload: { revision: number; state: Partial<MusicState> }) => void): () => void;
  onTheme(cb: (theme: Partial<HostTheme>) => void): () => void;
  send<T = Record<string, unknown>>(operation: string, args?: Record<string, unknown>): Promise<BridgeReply<T>>;
}

declare global {
  interface Window {
    quailcrate?: QuailCrateBridge;
  }
}

export function formatMs(ms: number): string {
  const totalSec = Math.max(0, Math.floor((Number(ms) || 0) / 1000));
  const mins = Math.floor(totalSec / 60);
  const secs = totalSec % 60;
  return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
}

export function swatchClass(color?: string): string {
  const allowed: Record<string, string> = {
    accent: 'swatch-accent',
    pink: 'swatch-pink',
    amber: 'swatch-amber',
    green: 'swatch-green',
    sky: 'swatch-sky',
    purple: 'swatch-purple',
  };
  return allowed[color || 'accent'] || 'swatch-accent';
}
