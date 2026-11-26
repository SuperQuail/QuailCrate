import { useCallback, useEffect, useState } from 'react';
import type { HostTheme, MusicState } from './types';

export const EMPTY_STATE: MusicState = {
  version: 1,
  locale: 'zh-CN',
  activeView: 'smart_all',
  sortBy: 'custom',
  preferredQuality: '192k',
  account: { loggedIn: false, nickname: '', mid: 0, level: 0, vip: false },
  login: { phase: 'idle', qrUrl: '', message: '' },
  playlists: [],
  tracks: [],
  recentIds: [],
  downloads: {},
  queue: [],
  playback: {
    trackId: '',
    status: 'idle',
    positionMs: 0,
    durationMs: 0,
    seekable: false,
    volume: 0.8,
    muted: false,
    mode: 'sequence',
    qualityLabel: '',
    error: '',
  },
  importPreview: null,
  notice: null,
};

const DEV_SAMPLE_STATE: MusicState = {
  ...EMPTY_STATE,
  playlists: [
    { id: 'pl_dev_1', name: '深夜代码', desc: 'Bilibili 轻音乐与 Lo-Fi 合集', color: 'accent', trackIds: ['tr_dev_1', 'tr_dev_2'] },
    { id: 'pl_dev_2', name: 'ACG 精选', desc: '动画原声与器乐编曲', color: 'pink', trackIds: ['tr_dev_2'] },
  ],
  tracks: [
    {
      id: 'tr_dev_1',
      dedupeKey: 'BV1GJ411x7h7:p1',
      bvid: 'BV1GJ411x7h7',
      aid: 80433022,
      cid: 137649199,
      page: 1,
      totalPages: 2,
      title: '星屑の夜想曲 (Dev Preview)',
      artist: '白键与海',
      durationMs: 255000,
      favorite: true,
      addedAt: 1764100000,
    },
    {
      id: 'tr_dev_2',
      dedupeKey: 'BV17x411w7KC:p1',
      bvid: 'BV17x411w7KC',
      aid: 170001,
      cid: 279786,
      page: 1,
      totalPages: 1,
      title: 'Midnight Rain · Piano Ver.',
      artist: 'Koto Studio',
      durationMs: 214000,
      favorite: false,
      addedAt: 1764101000,
    },
  ],
  queue: ['tr_dev_1', 'tr_dev_2'],
  playback: {
    trackId: 'tr_dev_1',
    status: 'paused',
    positionMs: 68000,
    durationMs: 255000,
    seekable: true,
    volume: 0.8,
    muted: false,
    mode: 'sequence',
    qualityLabel: '192K AAC',
    error: '',
  },
};

function hexToRgbTriplet(hex?: string): string {
  const clean = String(hex || '').replace('#', '');
  if (clean.length !== 6) return '203, 166, 247';
  const r = parseInt(clean.slice(0, 2), 16);
  const g = parseInt(clean.slice(2, 4), 16);
  const b = parseInt(clean.slice(4, 6), 16);
  if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) return '203, 166, 247';
  return `${r}, ${g}, ${b}`;
}

export function applyHostTheme(theme?: Partial<HostTheme> | null): void {
  if (!theme) return;
  const root = document.documentElement;
  const set = (name: string, val?: string) => {
    if (val) root.style.setProperty(name, val);
  };
  set('--bg', theme.background);
  set('--surface', theme.surface);
  set('--sidebar', theme.sidebar);
  set('--border', theme.border);
  set('--text', theme.text);
  set('--secondary', theme.secondary);
  set('--accent', theme.accent);
  set('--on-accent', theme.onAccent);
  set('--weekend', theme.weekend);
  set('--warning', theme.warning);
  set('--warning-bg', theme.warningBg);
  set('--selected', theme.selected);
  set('--hover', theme.hover);
  if (theme.accent) {
    root.style.setProperty('--accent-rgb', hexToRgbTriplet(theme.accent));
  }
  root.dataset.theme = theme.isDark === false ? 'light' : 'dark';
}

function mergeState(incoming?: Partial<MusicState> | null): MusicState {
  if (!incoming || typeof incoming !== 'object') return EMPTY_STATE;
  return {
    ...EMPTY_STATE,
    ...incoming,
    account: { ...EMPTY_STATE.account, ...(incoming.account || {}) },
    login: { ...EMPTY_STATE.login, ...(incoming.login || {}) },
    playback: { ...EMPTY_STATE.playback, ...(incoming.playback || {}) },
    playlists: Array.isArray(incoming.playlists) ? incoming.playlists : [],
    tracks: Array.isArray(incoming.tracks) ? incoming.tracks : [],
    recentIds: Array.isArray(incoming.recentIds) ? incoming.recentIds : [],
    downloads: incoming.downloads ?? {},
    queue: Array.isArray(incoming.queue) ? incoming.queue : [],
    importPreview: incoming.importPreview ?? null,
    notice: incoming.notice ?? null,
  };
}

export function useMusicBridge() {
  const hasHostBridge = typeof window !== 'undefined' && Boolean(window.quailcrate);

  const [state, setState] = useState<MusicState>(() => {
    if (typeof window !== 'undefined' && window.quailcrate) {
      return mergeState(window.quailcrate.getState());
    }
    return DEV_SAMPLE_STATE;
  });

  useEffect(() => {
    const bridge = window.quailcrate;
    if (!bridge) return;

    applyHostTheme(bridge.getTheme());
    const unsubState = bridge.onState((payload) => {
      if (payload?.state) {
        setState(mergeState(payload.state));
      }
    });
    const unsubTheme = bridge.onTheme((theme) => {
      applyHostTheme(theme);
    });

    bridge.send('library.refresh', {}).catch(() => {});
    return () => {
      unsubState();
      unsubTheme();
    };
  }, []);

  const previewDownloading = !hasHostBridge && Object.values(state.downloads).some((item) => item.status === 'downloading');
  useEffect(() => {
    if (!previewDownloading) return;
    const timer = window.setInterval(() => {
      setState((prev) => {
        const downloads = { ...prev.downloads };
        for (const [trackId, item] of Object.entries(downloads)) {
          if (item.status !== 'downloading') continue;
          const percent = (item.percent ?? 0) + 25;
          downloads[trackId] = percent < 100 ? { status: 'downloading', percent } : { status: 'downloaded' };
        }
        return { ...prev, downloads };
      });
    }, 500);
    return () => window.clearInterval(timer);
  }, [previewDownloading]);

  const send = useCallback(
    async <T = Record<string, unknown>>(operation: string, args: Record<string, unknown> = {}): Promise<T | undefined> => {
      const bridge = window.quailcrate;
      if (bridge) {
        try {
          const reply = await bridge.send<T>(operation, args);
          return reply?.value;
        } catch {
          return undefined;
        }
      }
      // Lightweight local preview behavior when developing outside QuailCrate via `pnpm dev`.
      setState((prev) => applyDevMockOp(prev, operation, args));
      return undefined;
    },
    [],
  );

  const setVolume = useCallback(async (volume: number, muted: boolean) => {
    const bridge = window.quailcrate;
    const args = { volume, muted };
    if (!bridge) {
      setState((prev) => applyDevMockOp(prev, 'playback.setVolume', args));
      return;
    }

    for (let attempt = 0; attempt < 2; attempt++) {
      const revision = bridge.getRevision();
      try {
        const reply = await bridge.send<{ ok: boolean }>('playback.setVolume', args);
        if (!reply.value?.ok) return;
        break;
      } catch {
        // Another player update can advance the page revision just before this request.
        if (attempt > 0 || revision === bridge.getRevision()) return;
      }
    }
    const playback = bridge.getState()?.playback;
    if (playback?.volume === volume && playback.muted === muted) return;

    // The host can deliver the reply before publishing the resulting page snapshot.
    // Keep the local thumb and the next request on the same side of that refresh.
    const lastRevision = bridge.getRevision();
    await new Promise<void>((resolve) => {
      const unsubscribe = bridge.onState(({ revision, state }) => {
        if (revision > lastRevision && state.playback?.volume === volume && state.playback.muted === muted) finish();
      });
      const timer = window.setTimeout(finish, 1500);
      function finish() {
        window.clearTimeout(timer);
        unsubscribe();
        resolve();
      }
    });
  }, []);

  return { state, send, hasHostBridge, setVolume };
}

function applyDevMockOp(prev: MusicState, op: string, args: Record<string, unknown>): MusicState {
  switch (op) {
    case 'view.select':
      return {
        ...prev,
        activeView: (args.viewId as string) || prev.activeView,
        sortBy: (args.sortBy as MusicState['sortBy']) || prev.sortBy,
      };
    case 'track.toggleFavorite':
      return {
        ...prev,
        tracks: prev.tracks.map((t) => (t.id === args.trackId ? { ...t, favorite: !t.favorite } : t)),
      };
    case 'playback.toggle':
      return {
        ...prev,
        playback: {
          ...prev.playback,
          status: prev.playback.status === 'playing' ? 'paused' : 'playing',
        },
      };
    case 'playback.playTrack': {
      const track = prev.tracks.find((item) => item.id === args.trackId);
      if (!track) return prev;
      if (args.toggleCurrent && track.id === prev.playback.trackId) {
        if (prev.playback.status === 'loading') return prev;
        if (prev.playback.status === 'playing' || prev.playback.status === 'paused') {
          return applyDevMockOp(prev, 'playback.toggle', {});
        }
      }
      return {
        ...prev,
        recentIds: [track.id, ...prev.recentIds.filter((id) => id !== track.id)],
        queue: Array.isArray(args.queueTrackIds) ? args.queueTrackIds : prev.queue,
        playback: {
          ...prev.playback,
          trackId: track.id,
          durationMs: track.durationMs,
          status: 'playing',
          positionMs: 0,
        },
      };
    }
    case 'playback.playPlaylist': {
      const viewId = args.viewId || prev.activeView;
      let ids: string[];
      switch (viewId) {
        case 'smart_all': ids = prev.tracks.map((track) => track.id); break;
        case 'smart_recent': ids = prev.recentIds; break;
        case 'smart_fav': ids = prev.tracks.filter((track) => track.favorite).map((track) => track.id); break;
        case 'smart_downloaded': ids = prev.tracks.filter((track) => prev.downloads[track.id]?.status === 'downloaded').map((track) => track.id); break;
        default: ids = prev.playlists.find((playlist) => playlist.id === viewId)?.trackIds ?? [];
      }
      return ids.length ? applyDevMockOp(prev, 'playback.playTrack', { trackId: ids[0], queueTrackIds: ids }) : prev;
    }
    case 'download.start': {
      const trackId = String(args.trackId);
      return { ...prev, downloads: { ...prev.downloads, [trackId]: { status: 'downloading', percent: 0 } } };
    }
    case 'download.remove': {
      const downloads = { ...prev.downloads };
      delete downloads[String(args.trackId)];
      return { ...prev, downloads };
    }
    case 'playback.seek':
      return {
        ...prev,
        playback: {
          ...prev.playback,
          positionMs: Number(args.positionMs ?? prev.playback.positionMs),
        },
      };
    case 'playback.setVolume':
      return {
        ...prev,
        playback: {
          ...prev.playback,
          volume: args.volume !== undefined ? Number(args.volume) : prev.playback.volume,
          muted: args.muted !== undefined ? Boolean(args.muted) : prev.playback.muted,
        },
      };
    case 'bilibili.setQuality':
      return { ...prev, preferredQuality: (args.quality as MusicState['preferredQuality']) || prev.preferredQuality };
    default:
      return prev;
  }
}
