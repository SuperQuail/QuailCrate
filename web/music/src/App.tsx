import { useEffect, useMemo, useState } from 'react';
import { useMusicBridge } from './bridge';
import { AccountModal } from './components/AccountModal';
import { ImportModal } from './components/ImportModal';
import { PlayerDock } from './components/PlayerDock';
import { PlaylistModal } from './components/PlaylistModal';
import { QueueDrawer } from './components/QueueDrawer';
import { Sidebar } from './components/Sidebar';
import { TrackList } from './components/TrackList';
import type { Playlist } from './types';

export function App() {
  const { state, send, setVolume } = useMusicBridge();

  const [search, setSearch] = useState('');
  const [queueOpen, setQueueOpen] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [playlistModal, setPlaylistModal] = useState<{ open: boolean; editing: Playlist | null }>({
    open: false,
    editing: null,
  });

  const currentTrack = useMemo(
    () => state.tracks.find((t) => t.id === state.playback.trackId) ?? null,
    [state.tracks, state.playback.trackId],
  );

  const activePlaylistId = useMemo(
    () => (state.playlists.some((p) => p.id === state.activeView) ? state.activeView : ''),
    [state.playlists, state.activeView],
  );

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setQueueOpen(false);
        setAccountOpen(false);
        setImportOpen(false);
        setPlaylistModal({ open: false, editing: null });
        return;
      }
      if (e.key === ' ') {
        const tag = (e.target as HTMLElement | null)?.tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || tag === 'BUTTON') return;
        e.preventDefault();
        void send('playback.toggle');
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [send]);

  const acc = state.account;

  return (
    <div className="app-shell">
      <div className="app-body">
        <Sidebar
          state={state}
          search={search}
          onSearchChange={setSearch}
          onSelectView={(viewId) => void send('view.select', { viewId })}
          onOpenCreatePlaylist={() => setPlaylistModal({ open: true, editing: null })}
        />

        <main className="main-pane">
          <header className="main-header">
            <div className="header-left">
              <span className="header-kicker">QUAILCRATE MUSIC // BILIBILI STREAMING</span>
            </div>
            <button
              type="button"
              className={`auth-pill${acc.loggedIn ? ' logged-in' : ''}`}
              onClick={() => {
                setAccountOpen(true);
                if (!acc.loggedIn && state.login.phase === 'idle') {
                  void send('bilibili.loginStart');
                }
              }}
            >
              <span className="status-dot" />
              <span>
                {acc.loggedIn
                  ? `${acc.nickname || '已登录'}${acc.vip ? ' · 大会员' : ''}`
                  : 'B站 未登录'}
              </span>
            </button>
          </header>

          <TrackList
            state={state}
            search={search}
            onPlayAll={() => void send('playback.playPlaylist', { viewId: state.activeView })}
            onPlayTrack={(trackId, queueTrackIds, toggleCurrent) =>
              void send('playback.playTrack', { trackId, queueTrackIds, toggleCurrent })
            }
            onStartDownload={(trackId) => void send('download.start', { trackId })}
            onRemoveDownload={(trackId) => void send('download.remove', { trackId })}
            onToggleFav={(trackId) => void send('track.toggleFavorite', { trackId })}
            onSortChange={(sortBy) => void send('view.select', { viewId: state.activeView, sortBy })}
            onOpenImport={() => {
              void send('bilibili.clearPreview');
              setImportOpen(true);
            }}
            onEditPlaylist={(pl) => setPlaylistModal({ open: true, editing: pl })}
            onDeletePlaylist={(playlistId) => void send('playlist.delete', { playlistId })}
            onAddToQueue={(trackId) => void send('queue.add', { trackId })}
            onPlayNext={(trackId) => void send('queue.playNext', { trackId })}
            onAddToPlaylist={(playlistId, trackId) =>
              void send('playlist.addTracks', { playlistId, trackIds: [trackId] })
            }
            onRemoveFromPlaylist={(playlistId, trackId) =>
              void send('playlist.removeTrack', { playlistId, trackId })
            }
            onDeleteTrack={(trackId) => void send('track.delete', { trackId })}
            onDismissNotice={() => {}}
          />
        </main>
      </div>

      <PlayerDock
        state={state}
        currentTrack={currentTrack}
        queueOpen={queueOpen}
        onTogglePlay={() => void send('playback.toggle')}
        onPrev={() => void send('playback.prev')}
        onNext={() => void send('playback.next')}
        onCycleMode={() => void send('playback.cycleMode')}
        onSeekCommit={(positionMs) => void send('playback.seek', { positionMs })}
        onVolumeChange={setVolume}
        onToggleQueue={() => setQueueOpen((v) => !v)}
      />

      <QueueDrawer
        open={queueOpen}
        state={state}
        onClose={() => setQueueOpen(false)}
        onPlayTrack={(trackId) => void send('playback.playTrack', { trackId })}
        onRemoveTrack={(trackId) => void send('queue.remove', { trackId })}
        onClearQueue={() => void send('queue.clear')}
      />

      <AccountModal
        open={accountOpen}
        state={state}
        onClose={() => setAccountOpen(false)}
        onStartLogin={() => void send('bilibili.loginStart')}
        onCancelLogin={() => void send('bilibili.loginCancel')}
        onRefreshAccount={() => void send('bilibili.loginRefresh')}
        onLogout={() => void send('bilibili.logout')}
        onSelectQuality={(quality) => void send('bilibili.setQuality', { quality })}
      />

      <ImportModal
        open={importOpen}
        state={state}
        defaultPlaylistId={activePlaylistId}
        onClose={() => {
          setImportOpen(false);
          void send('bilibili.clearPreview');
        }}
        onPreview={(input) => void send('bilibili.preview', { input })}
        onCommitImport={(playlistId, items) =>
          void send('bilibili.import', { playlistId, items })
        }
      />

      <PlaylistModal
        open={playlistModal.open}
        editing={playlistModal.editing}
        onClose={() => setPlaylistModal({ open: false, editing: null })}
        onSubmit={(name, desc, color, playlistId) => {
          if (playlistId) {
            void send('playlist.update', { playlistId, name, desc, color });
          } else {
            void send('playlist.create', { name, desc, color });
          }
        }}
      />
    </div>
  );
}
