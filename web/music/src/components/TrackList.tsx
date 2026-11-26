import { useMemo, useState } from 'react';
import {
  Disc3,
  Download,
  Heart,
  ListEnd,
  ListPlus,
  MoreHorizontal,
  Pause,
  Pencil,
  Play,
  Plus,
  RotateCw,
  Trash2,
  X,
} from 'lucide-react';
import {
  formatMs,
  swatchClass,
  type MusicState,
  type Playlist,
  type SortBy,
  type Track,
  type TrackDownload,
} from '../types';

interface TrackListProps {
  state: MusicState;
  search: string;
  onPlayAll: () => void;
  onPlayTrack: (trackId: string, queueIds: string[], toggleCurrent?: boolean) => void;
  onStartDownload: (trackId: string) => void;
  onRemoveDownload: (trackId: string) => void;
  onToggleFav: (trackId: string) => void;
  onSortChange: (sortBy: SortBy) => void;
  onOpenImport: () => void;
  onEditPlaylist: (pl: Playlist) => void;
  onDeletePlaylist: (playlistId: string) => void;
  onAddToQueue: (trackId: string) => void;
  onPlayNext: (trackId: string) => void;
  onAddToPlaylist: (playlistId: string, trackId: string) => void;
  onRemoveFromPlaylist: (playlistId: string, trackId: string) => void;
  onDeleteTrack: (trackId: string) => void;
  onDismissNotice: () => void;
}

const SORT_OPTIONS: { id: SortBy; label: string }[] = [
  { id: 'custom', label: '默认' },
  { id: 'title', label: '标题' },
  { id: 'artist', label: 'UP 主' },
  { id: 'duration', label: '时长' },
];

function downloadLabel(download: TrackDownload) {
  if (download.status === 'downloaded') return '已下载';
  if (download.status === 'error') return '下载失败';
  return download.percent == null ? '下载中' : `下载中 ${Math.round(download.percent)}%`;
}

export function TrackList({
  state,
  search,
  onPlayAll,
  onPlayTrack,
  onStartDownload,
  onRemoveDownload,
  onToggleFav,
  onSortChange,
  onOpenImport,
  onEditPlaylist,
  onDeletePlaylist,
  onAddToQueue,
  onPlayNext,
  onAddToPlaylist,
  onRemoveFromPlaylist,
  onDeleteTrack,
  onDismissNotice,
}: TrackListProps) {
  const [menuState, setMenuState] = useState<{ track: Track; x: number; y: number } | null>(null);
  const [dismissedNotice, setDismissedNotice] = useState<string>('');
  const isDownloadsView = state.activeView === 'smart_downloaded';
  const menuDownload = menuState ? state.downloads[menuState.track.id] : undefined;

  const activePlaylist = useMemo(
    () => state.playlists.find((p) => p.id === state.activeView) ?? null,
    [state.playlists, state.activeView],
  );

  const trackMap = useMemo(() => {
    const map = new Map<string, Track>();
    for (const t of state.tracks) map.set(t.id, t);
    return map;
  }, [state.tracks]);

  const visibleTracks = useMemo(() => {
    let base: Track[] = [];
    if (state.activeView === 'smart_all') {
      base = [...state.tracks];
    } else if (state.activeView === 'smart_fav') {
      base = state.tracks.filter((t) => t.favorite);
    } else if (state.activeView === 'smart_recent') {
      base = state.recentIds.map((id) => trackMap.get(id)).filter((t): t is Track => Boolean(t));
    } else if (isDownloadsView) {
      base = state.tracks.filter((track) => state.downloads[track.id]);
    } else if (activePlaylist) {
      base = activePlaylist.trackIds.map((id) => trackMap.get(id)).filter((t): t is Track => Boolean(t));
    }

    const q = search.trim().toLowerCase();
    if (q) {
      base = base.filter(
        (t) =>
          t.title.toLowerCase().includes(q) ||
          t.artist.toLowerCase().includes(q) ||
          t.bvid.toLowerCase().includes(q),
      );
    }

    if (state.sortBy === 'title') {
      base.sort((a, b) => a.title.localeCompare(b.title, 'zh-CN'));
    } else if (state.sortBy === 'artist') {
      base.sort((a, b) => a.artist.localeCompare(b.artist, 'zh-CN'));
    } else if (state.sortBy === 'duration') {
      base.sort((a, b) => b.durationMs - a.durationMs);
    }
    return base;
  }, [state.activeView, state.tracks, state.recentIds, state.sortBy, state.downloads, isDownloadsView, activePlaylist, trackMap, search]);

  const totalMs = useMemo(() => visibleTracks.reduce((sum, t) => sum + (t.durationMs || 0), 0), [visibleTracks]);

  const heroMeta = useMemo(() => {
    if (activePlaylist) {
      return {
        title: activePlaylist.name,
        desc: activePlaylist.desc || '自定义 Bilibili 音频歌单',
        color: activePlaylist.color,
      };
    }
    if (state.activeView === 'smart_fav') {
      return { title: '我喜欢的', desc: '收藏的高频循环曲目', color: 'pink' };
    }
    if (state.activeView === 'smart_recent') {
      return { title: '最近播放', desc: '按最近播放时间倒序记录', color: 'sky' };
    }
    if (isDownloadsView) {
      return { title: '已下载', desc: '下载完成后，断网也能播放。', color: 'green' };
    }
    return {
      title: '全部曲目',
      color: 'accent',
    };
  }, [activePlaylist, state.activeView, isDownloadsView]);

  const showNotice = Boolean(state.notice?.message && state.notice.message !== dismissedNotice);
  const queueIds = useMemo(
    () => visibleTracks.filter((t) => !isDownloadsView || state.downloads[t.id]?.status === 'downloaded').map((t) => t.id),
    [visibleTracks, isDownloadsView, state.downloads],
  );

  return (
    <div className="main-scroll">
      {showNotice && state.notice && (
        <div className={`notice-banner${state.notice.kind === 'error' ? ' error' : ''}`}>
          <span>{state.notice.message}</span>
          <button
            type="button"
            className="row-btn"
            onClick={() => {
              setDismissedNotice(state.notice?.message || '');
              onDismissNotice();
            }}
          >
            <X size={13} />
          </button>
        </div>
      )}

      <section className="hero-card">
        <div className={`cover-art ${swatchClass(heroMeta.color)}`}>
          {isDownloadsView ? <Download size={34} /> : <Disc3 size={34} />}
        </div>
        <div className="hero-info">
          <h1 className="hero-title">{heroMeta.title}</h1>
          {heroMeta.desc && <p className="hero-desc">{heroMeta.desc}</p>}
          <div className="hero-stats">
            {visibleTracks.length} 首曲目 · 总时长 {formatMs(totalMs)}
          </div>
          <div className="hero-actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={queueIds.length === 0}
              onClick={onPlayAll}
            >
              <Play size={13} fill="currentColor" />
              <span>播放全部</span>
            </button>
            {!isDownloadsView && (
              <button type="button" className="btn btn-tonal" onClick={onOpenImport}>
                <Plus size={14} />
                <span>导入 BV / 收藏夹</span>
              </button>
            )}
            {activePlaylist && (
              <>
                <button type="button" className="btn" onClick={() => onEditPlaylist(activePlaylist)}>
                  <Pencil size={13} />
                  <span>编辑歌单</span>
                </button>
                <button
                  type="button"
                  className="btn btn-danger"
                  onClick={() => onDeletePlaylist(activePlaylist.id)}
                >
                  <Trash2 size={13} />
                  <span>删除歌单</span>
                </button>
              </>
            )}
          </div>
        </div>
      </section>

      <div className="list-toolbar">
        <span className="toolbar-label">
          {search.trim() ? `筛选结果：${visibleTracks.length} 首` : `共 ${visibleTracks.length} 首曲目`}
        </span>
        <div className="sort-group">
          {SORT_OPTIONS.map((opt) => (
            <button
              key={opt.id}
              type="button"
              className={`sort-chip${state.sortBy === opt.id ? ' active' : ''}`}
              onClick={() => onSortChange(opt.id)}
            >
              {opt.label}
            </button>
          ))}
        </div>
      </div>

      <div className="track-table">
        {visibleTracks.length === 0 ? (
          <div className="empty-state">
            <div className="empty-icon">
              <Disc3 size={24} />
            </div>
            <div className="empty-title">
              {search.trim() ? '未找到匹配的曲目' : isDownloadsView ? '暂无下载' : '当前歌单暂无曲目'}
            </div>
            <div className="empty-desc">
              {search.trim()
                ? '尝试更换关键词，或清空左侧搜索栏查看完整曲库。'
                : isDownloadsView ? '在曲目的「更多操作」中选择「下载到本机」。'
                : '粘贴 Bilibili BV/AV 号、视频链接、b23.tv 短链或收藏夹链接即可导入并流式播放。'}
            </div>
            {!search.trim() && !isDownloadsView && (
              <button type="button" className="btn btn-primary" onClick={onOpenImport}>
                <Plus size={14} />
                <span>导入第一首曲目</span>
              </button>
            )}
          </div>
        ) : (
          <>
            <div className="track-head">
              <span>#</span>
              <span>标题</span>
              <span className="col-artist">UP 主</span>
              <span>时长</span>
              <span />
            </div>
            {visibleTracks.map((track, idx) => {
              const isCurrent = state.playback.trackId === track.id;
              const isPlaying = isCurrent && state.playback.status === 'playing';
              const isPaused = isCurrent && state.playback.status === 'paused';
              const isLoading = isCurrent && state.playback.status === 'loading';
              const download = state.downloads[track.id];
              const available = !isDownloadsView || download?.status === 'downloaded' || isPlaying || isPaused;
              const actionLabel = isPlaying ? '暂停' : isPaused ? '继续播放' : isLoading ? '正在加载' : '播放';
              return (
                <div
                  key={track.id}
                  className={`track-row${isCurrent ? ' playing' : ''}`}
                  onDoubleClick={(e) => {
                    if (available && !isLoading && !(e.target as Element).closest('button')) {
                      onPlayTrack(track.id, queueIds, true);
                    }
                  }}
                >
                  <button
                    type="button"
                    className="track-idx"
                    disabled={isLoading || !available}
                    title={available ? actionLabel : '等待下载完成'}
                    aria-label={`${actionLabel}《${track.title}》`}
                    onClick={(e) => {
                      if (e.detail <= 1) onPlayTrack(track.id, queueIds, true);
                    }}
                  >
                    <span className="track-idx-rest" aria-hidden="true">
                      {isPlaying ? (
                        <span className="eq-bars">
                          <span />
                          <span />
                          <span />
                        </span>
                      ) : isPaused ? <Play size={13} fill="currentColor" /> : idx + 1}
                    </span>
                    <span className="track-idx-action" aria-hidden="true">
                      {isPlaying ? <Pause size={13} fill="currentColor" /> : <Play size={13} fill="currentColor" />}
                    </span>
                  </button>

                  <div className="track-main">
                    <div className="track-title" title={track.title}>
                      {track.title}
                    </div>
                    <div className="track-meta">
                      <span>{track.bvid}</span>
                      {track.totalPages > 1 && <span className="meta-tag">P{track.page}</span>}
                      {download && (
                        <span className={`download-status${download.status === 'error' ? ' error' : ''}`} title={download.error}>
                          {downloadLabel(download)}
                        </span>
                      )}
                      {download?.status === 'error' && (
                        <button type="button" className="download-retry" onClick={() => onStartDownload(track.id)}>
                          重试
                        </button>
                      )}
                    </div>
                  </div>

                  <div className="track-artist col-artist" title={track.artist}>
                    {track.artist || 'Bilibili'}
                  </div>

                  <div className="track-dur">{formatMs(track.durationMs)}</div>

                  <div className="track-actions">
                    <button
                      type="button"
                      className={`row-btn${track.favorite ? ' fav-active' : ''}`}
                      title={track.favorite ? '取消收藏' : '收藏'}
                      onClick={(e) => {
                        e.stopPropagation();
                        onToggleFav(track.id);
                      }}
                    >
                      <Heart size={14} fill={track.favorite ? 'currentColor' : 'none'} />
                    </button>
                    <button
                      type="button"
                      className="row-btn"
                      title="下一首播放"
                      onClick={(e) => {
                        e.stopPropagation();
                        onPlayNext(track.id);
                      }}
                    >
                      <ListPlus size={14} />
                    </button>
                    <button
                      type="button"
                      className="row-btn"
                      title="更多操作"
                      onClick={(e) => {
                        e.stopPropagation();
                        const rect = e.currentTarget.getBoundingClientRect();
                        const menuW = 195;
                        const menuH = 300;
                        const x = Math.max(8, Math.min(window.innerWidth - menuW - 8, rect.right - menuW));
                        const y = Math.max(8, Math.min(window.innerHeight - menuH - 8, rect.bottom + 4));
                        setMenuState({ track, x, y });
                      }}
                    >
                      <MoreHorizontal size={14} />
                    </button>
                  </div>
                </div>
              );
            })}
          </>
        )}
      </div>

      {menuState && (
        <>
          <div className="row-popover-backdrop" onClick={() => setMenuState(null)} />
          <div
            className="row-popover"
            style={{ left: `${menuState.x}px`, top: `${menuState.y}px` }}
          >
            <button
              type="button"
              className="popover-item"
              disabled={isDownloadsView && menuDownload?.status !== 'downloaded'}
              onClick={() => {
                onPlayTrack(menuState.track.id, queueIds);
                setMenuState(null);
              }}
            >
              <Play size={13} />
              <span>立即播放</span>
            </button>
            <button
              type="button"
              className="popover-item"
              onClick={() => {
                onPlayNext(menuState.track.id);
                setMenuState(null);
              }}
            >
              <ListPlus size={13} />
              <span>插为下一首</span>
            </button>
            <button
              type="button"
              className="popover-item"
              onClick={() => {
                onAddToQueue(menuState.track.id);
                setMenuState(null);
              }}
            >
              <ListEnd size={13} />
              <span>追加到队列末尾</span>
            </button>
            {menuDownload?.status === 'downloaded' ? (
              <button
                type="button"
                className="popover-item"
                onClick={() => {
                  onRemoveDownload(menuState.track.id);
                  setMenuState(null);
                }}
              >
                <X size={13} />
                <span>移除下载</span>
              </button>
            ) : (
              <button
                type="button"
                className="popover-item"
                disabled={menuDownload?.status === 'downloading'}
                onClick={() => {
                  onStartDownload(menuState.track.id);
                  setMenuState(null);
                }}
              >
                {menuDownload?.status === 'error' ? <RotateCw size={13} /> : <Download size={13} />}
                <span>{menuDownload?.status === 'error' ? '重新下载' : menuDownload ? downloadLabel(menuDownload) : '下载到本机'}</span>
              </button>
            )}

            {state.playlists.length > 0 && (
              <>
                <div className="popover-sep" />
                <div className="popover-heading">加入歌单</div>
                {state.playlists.map((pl) => (
                  <button
                    key={pl.id}
                    type="button"
                    className="popover-item"
                    onClick={() => {
                      onAddToPlaylist(pl.id, menuState.track.id);
                      setMenuState(null);
                    }}
                  >
                    <span className={`playlist-dot ${swatchClass(pl.color)}`} />
                    <span className="nav-label">{pl.name}</span>
                  </button>
                ))}
              </>
            )}

            <div className="popover-sep" />
            {activePlaylist && (
              <button
                type="button"
                className="popover-item danger"
                onClick={() => {
                  onRemoveFromPlaylist(activePlaylist.id, menuState.track.id);
                  setMenuState(null);
                }}
              >
                <X size={13} />
                <span>从当前歌单移除</span>
              </button>
            )}
            <button
              type="button"
              className="popover-item danger"
              onClick={() => {
                onDeleteTrack(menuState.track.id);
                setMenuState(null);
              }}
            >
              <Trash2 size={13} />
              <span>从曲库彻底删除</span>
            </button>
          </div>
        </>
      )}
    </div>
  );
}
