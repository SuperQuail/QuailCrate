import { useEffect, useState } from 'react';
import { X } from 'lucide-react';
import { swatchClass, type Playlist, type SwatchColor } from '../types';

interface PlaylistModalProps {
  open: boolean;
  editing: Playlist | null;
  onClose: () => void;
  onSubmit: (name: string, desc: string, color: SwatchColor, playlistId?: string) => void;
}

const SWATCHES: SwatchColor[] = ['accent', 'pink', 'amber', 'green', 'sky', 'purple'];

export function PlaylistModal({ open, editing, onClose, onSubmit }: PlaylistModalProps) {
  const [name, setName] = useState('');
  const [desc, setDesc] = useState('');
  const [color, setColor] = useState<SwatchColor>('accent');

  useEffect(() => {
    if (!open) return;
    if (editing) {
      setName(editing.name);
      setDesc(editing.desc || '');
      setColor((editing.color as SwatchColor) || 'accent');
    } else {
      setName('');
      setDesc('');
      setColor('accent');
    }
  }, [open, editing]);

  if (!open) return null;

  const submit = () => {
    const trimmed = name.trim();
    if (!trimmed) return;
    onSubmit(trimmed, desc.trim(), color, editing?.id);
    onClose();
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <h2 className="modal-title">{editing ? '编辑歌单' : '新建歌单'}</h2>
          <button type="button" className="row-btn" onClick={onClose}>
            <X size={15} />
          </button>
        </div>

        <div className="modal-body">
          <div className="field">
            <label className="field-label" htmlFor="pl-name-input">歌单名称</label>
            <input
              id="pl-name-input"
              type="text"
              className="text-input"
              placeholder="例如：深夜代码、游戏原声…"
              maxLength={80}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submit();
              }}
              autoFocus
            />
          </div>

          <div className="field">
            <label className="field-label" htmlFor="pl-desc-input">简介（可选）</label>
            <input
              id="pl-desc-input"
              type="text"
              className="text-input"
              placeholder="记录这个歌单的氛围或来源"
              maxLength={200}
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
            />
          </div>

          <div className="field">
            <span className="field-label">封面色标</span>
            <div className="swatch-row">
              {SWATCHES.map((sw) => (
                <button
                  key={sw}
                  type="button"
                  className={`swatch-btn ${swatchClass(sw)}${color === sw ? ' active' : ''}`}
                  title={sw}
                  onClick={() => setColor(sw)}
                />
              ))}
            </div>
          </div>
        </div>

        <div className="modal-footer">
          <button type="button" className="btn" onClick={onClose}>
            取消
          </button>
          <button type="button" className="btn btn-primary" disabled={!name.trim()} onClick={submit}>
            {editing ? '保存修改' : '创建歌单'}
          </button>
        </div>
      </div>
    </div>
  );
}
