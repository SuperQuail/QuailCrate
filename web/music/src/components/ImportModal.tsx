import { useEffect, useState } from 'react';
import { Download, Loader2, Search, X } from 'lucide-react';
import { formatMs, type ImportPreviewItem, type MusicState } from '../types';

interface ImportModalProps {
  open: boolean;
  state: MusicState;
  defaultPlaylistId: string;
  onClose: () => void;
  onPreview: (input: string) => void;
  onCommitImport: (playlistId: string, items: ImportPreviewItem[]) => void;
}

export function ImportModal({
  open,
  state,
  defaultPlaylistId,
  onClose,
  onPreview,
  onCommitImport,
}: ImportModalProps) {
  const [input, setInput] = useState('');
  const [targetPlaylist, setTargetPlaylist] = useState(defaultPlaylistId);
  const [checkedKeys, setCheckedKeys] = useState<Set<string>>(new Set());

  const preview = state.importPreview;

  useEffect(() => {
    if (open) {
      setTargetPlaylist(defaultPlaylistId);
    }
  }, [open, defaultPlaylistId]);

  useEffect(() => {
    if (!preview || preview.status !== 'ready') return;
    const next = new Set<string>();
    for (const it of preview.items) {
      if (it.selected !== false) {
        next.add(it.dedupeKey);
      }
    }
    setCheckedKeys(next);
  }, [preview]);

  if (!open) return null;

  const isLoading = preview?.status === 'loading';
  const items = preview?.items || [];
  const selectedItems = items.filter((it) => checkedKeys.has(it.dedupeKey));

  const toggleKey = (key: string) => {
    setCheckedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card modal-card-wide" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <h2 className="modal-title">导入 Bilibili 曲目 / 分 P / 收藏夹</h2>
          <button type="button" className="row-btn" onClick={onClose}>
            <X size={15} />
          </button>
        </div>

        <div className="modal-body">
          <div className="field">
            <label className="field-label" htmlFor="bili-import-input">
              粘贴 BV/AV 号、视频链接（支持 ?p=N）、b23.tv 短链或收藏夹链接（每行一条）
            </label>
            <textarea
              id="bili-import-input"
              className="text-area"
              placeholder={'BV1GJ411x7h7\nhttps://www.bilibili.com/video/BV17x411w7KC?p=2\nhttps://space.bilibili.com/12345/favlist?fid=987654'}
              value={input}
              onChange={(e) => setInput(e.target.value)}
            />
          </div>

          <div className="header-actions">
            <button
              type="button"
              className="btn btn-tonal"
              disabled={!input.trim() || isLoading}
              onClick={() => onPreview(input)}
            >
              {isLoading ? <Loader2 size={13} /> : <Search size={13} />}
              <span>{isLoading ? '正在解析…' : '解析预览'}</span>
            </button>

            <select
              className="select-input"
              value={targetPlaylist}
              onChange={(e) => setTargetPlaylist(e.target.value)}
            >
              <option value="">仅加入全部曲库</option>
              {state.playlists.map((pl) => (
                <option key={pl.id} value={pl.id}>
                  同时加入歌单：{pl.name}
                </option>
              ))}
            </select>
          </div>

          {preview && preview.errors.length > 0 && (
            <div className="preview-errors">
              {preview.errors.map((err, i) => (
                <div key={i}>• {err}</div>
              ))}
            </div>
          )}

          {preview && items.length > 0 && (
            <div className="preview-box">
              <div className="preview-bar">
                <span>
                  {preview.sourceTitle ? `${preview.sourceTitle} · ` : ''}
                  已选 {selectedItems.length} / {items.length} 首
                </span>
                <div className="header-actions">
                  <button
                    type="button"
                    className="btn btn-sm"
                    onClick={() => setCheckedKeys(new Set(items.map((it) => it.dedupeKey)))}
                  >
                    全选
                  </button>
                  <button type="button" className="btn btn-sm" onClick={() => setCheckedKeys(new Set())}>
                    全不选
                  </button>
                </div>
              </div>
              <div className="preview-list">
                {items.map((it) => {
                  const checked = checkedKeys.has(it.dedupeKey);
                  return (
                    <label key={it.dedupeKey} className="preview-row">
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => toggleKey(it.dedupeKey)}
                      />
                      <div className="preview-info">
                        <div className="preview-title">{it.title}</div>
                        <div className="preview-sub">
                          {it.artist || 'Bilibili'} · {it.bvid}
                          {it.totalPages > 1 ? ` · P${it.page}` : ''} · {formatMs(it.durationMs)}
                          {it.alreadyInLibrary ? ' · 已在曲库' : ''}
                        </div>
                      </div>
                    </label>
                  );
                })}
              </div>
            </div>
          )}
        </div>

        <div className="modal-footer">
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={selectedItems.length === 0 || isLoading}
            onClick={() => {
              onCommitImport(targetPlaylist, selectedItems);
              setInput('');
              onClose();
            }}
          >
            <Download size={13} />
            <span>导入已选 ({selectedItems.length})</span>
          </button>
        </div>
      </div>
    </div>
  );
}
