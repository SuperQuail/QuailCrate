import { ListMusic, Trash2, X } from 'lucide-react';
import { formatMs, type MusicState, type Track } from '../types';

interface QueueDrawerProps {
  open: boolean;
  state: MusicState;
  onClose: () => void;
  onPlayTrack: (trackId: string) => void;
  onRemoveTrack: (trackId: string) => void;
  onClearQueue: () => void;
}

export function QueueDrawer({
  open,
  state,
  onClose,
  onPlayTrack,
  onRemoveTrack,
  onClearQueue,
}: QueueDrawerProps) {
  if (!open) return null;

  const trackMap = new Map<string, Track>();
  for (const t of state.tracks) trackMap.set(t.id, t);
  const items = state.queue.map((id) => trackMap.get(id)).filter((t): t is Track => Boolean(t));

  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <aside className="queue-drawer" onClick={(e) => e.stopPropagation()}>
        <div className="drawer-header">
          <div className="drawer-title">
            <ListMusic size={15} />
            <span>播放队列 ({items.length})</span>
          </div>
          <div className="header-actions">
            {items.length > 0 && (
              <button type="button" className="btn btn-sm btn-danger" onClick={onClearQueue}>
                <Trash2 size={12} />
                <span>清空</span>
              </button>
            )}
            <button type="button" className="row-btn" title="关闭队列" onClick={onClose}>
              <X size={15} />
            </button>
          </div>
        </div>

        <div className="drawer-list">
          {items.length === 0 ? (
            <div className="empty-state">
              <div className="empty-desc">播放队列为空</div>
            </div>
          ) : (
            items.map((track) => {
              const playing = state.playback.trackId === track.id;
              return (
                <div
                  key={track.id}
                  className={`queue-item${playing ? ' playing' : ''}`}
                  onClick={() => onPlayTrack(track.id)}
                >
                  <div className="queue-item-info">
                    <div className="queue-item-title">{track.title}</div>
                    <div className="queue-item-sub">
                      {track.artist || 'Bilibili'} · {formatMs(track.durationMs)}
                    </div>
                  </div>
                  <button
                    type="button"
                    className="row-btn danger"
                    title="从队列移除"
                    onClick={(e) => {
                      e.stopPropagation();
                      onRemoveTrack(track.id);
                    }}
                  >
                    <X size={13} />
                  </button>
                </div>
              );
            })
          )}
        </div>
      </aside>
    </div>
  );
}
