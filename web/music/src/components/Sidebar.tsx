import { Clock, Download, Heart, Library, Plus, Search, X } from 'lucide-react';
import { swatchClass, type MusicState } from '../types';

interface SidebarProps {
  state: MusicState;
  search: string;
  onSearchChange: (q: string) => void;
  onSelectView: (viewId: string) => void;
  onOpenCreatePlaylist: () => void;
}

export function Sidebar({
  state,
  search,
  onSearchChange,
  onSelectView,
  onOpenCreatePlaylist,
}: SidebarProps) {
  const favCount = state.tracks.filter((t) => t.favorite).length;

  return (
    <aside className="sidebar">
      <div className="sidebar-top">
        <div className="search-box">
          <span className="search-icon">
            <Search size={14} />
          </span>
          <input
            type="text"
            className="search-input"
            placeholder="搜索标题、UP 主、BV 号…"
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
          />
          {search && (
            <button
              type="button"
              className="search-clear"
              title="清除搜索"
              onClick={() => onSearchChange('')}
            >
              <X size={12} />
            </button>
          )}
        </div>
      </div>

      <div className="sidebar-scroll">
        <div className="nav-section-title">曲库视图</div>
        <button
          type="button"
          className={`nav-item${state.activeView === 'smart_all' ? ' active' : ''}`}
          onClick={() => onSelectView('smart_all')}
        >
          <span className="nav-icon"><Library size={15} /></span>
          <span className="nav-label">全部曲目</span>
          <span className="nav-count">{state.tracks.length}</span>
        </button>

        <button
          type="button"
          className={`nav-item${state.activeView === 'smart_fav' ? ' active' : ''}`}
          onClick={() => onSelectView('smart_fav')}
        >
          <span className="nav-icon"><Heart size={15} /></span>
          <span className="nav-label">我喜欢的</span>
          <span className="nav-count">{favCount}</span>
        </button>

        <button
          type="button"
          className={`nav-item${state.activeView === 'smart_recent' ? ' active' : ''}`}
          onClick={() => onSelectView('smart_recent')}
        >
          <span className="nav-icon"><Clock size={15} /></span>
          <span className="nav-label">最近播放</span>
          <span className="nav-count">{state.recentIds.length}</span>
        </button>

        <button
          type="button"
          className={`nav-item${state.activeView === 'smart_downloaded' ? ' active' : ''}`}
          onClick={() => onSelectView('smart_downloaded')}
        >
          <span className="nav-icon"><Download size={15} /></span>
          <span className="nav-label">已下载</span>
          <span className="nav-count">{Object.values(state.downloads).filter((d) => d.status === 'downloaded').length}</span>
        </button>

        <div className="nav-section-title">
          <span>自定义歌单</span>
          <span>{state.playlists.length}</span>
        </div>

        {state.playlists.map((pl) => (
          <button
            key={pl.id}
            type="button"
            className={`nav-item${state.activeView === pl.id ? ' active' : ''}`}
            onClick={() => onSelectView(pl.id)}
          >
            <span className={`playlist-dot ${swatchClass(pl.color)}`} />
            <span className="nav-label">{pl.name}</span>
            <span className="nav-count">{pl.trackIds.length}</span>
          </button>
        ))}
      </div>

      <div className="sidebar-footer">
        <button type="button" className="btn-create-playlist" onClick={onOpenCreatePlaylist}>
          <Plus size={14} />
          <span>新建歌单</span>
        </button>
      </div>
    </aside>
  );
}
