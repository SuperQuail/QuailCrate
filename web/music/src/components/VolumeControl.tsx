import { useEffect, useRef, useState } from 'react';
import { Volume2, VolumeX } from 'lucide-react';

interface Volume {
  volume: number;
  muted: boolean;
}

interface VolumeControlProps extends Volume {
  onCommit: (volume: number, muted: boolean) => Promise<void>;
}

export function VolumeControl({ volume, muted, onCommit }: VolumeControlProps) {
  const [draft, setDraft] = useState<Volume | null>(null);
  const queued = useRef<Volume | null>(null);
  const sending = useRef(false);
  const timer = useRef<number | undefined>(undefined);
  const shown = draft ?? { volume, muted };

  useEffect(() => () => {
    window.clearTimeout(timer.current);
    queued.current = null;
  }, []);

  async function flush() {
    window.clearTimeout(timer.current);
    timer.current = undefined;
    if (sending.current) return;
    sending.current = true;
    try {
      while (queued.current && timer.current === undefined) {
        const next = queued.current;
        queued.current = null;
        await onCommit(next.volume, next.muted);
      }
    } finally {
      sending.current = false;
      if (!queued.current) setDraft(null);
    }
  }

  function change(next: Volume, immediate = false) {
    setDraft(next);
    queued.current = next;
    if (immediate) {
      void flush();
    } else if (timer.current === undefined) {
      timer.current = window.setTimeout(flush, 80);
    }
  }

  return (
    <div className="vol-wrap">
      <button
        type="button"
        className="ctrl-btn"
        title={shown.muted ? '取消静音' : '静音'}
        onClick={() => change({ ...shown, muted: !shown.muted }, true)}
      >
        {shown.muted || shown.volume === 0 ? <VolumeX size={15} /> : <Volume2 size={15} />}
      </button>
      <input
        type="range"
        className="vol-slider"
        aria-label="音量"
        min={0}
        max={100}
        value={Math.round((shown.muted ? 0 : shown.volume) * 100)}
        onChange={(e) => change({ volume: Number(e.currentTarget.value) / 100, muted: false })}
        onPointerUp={() => void flush()}
        onPointerCancel={() => void flush()}
        onKeyUp={() => void flush()}
        onBlur={() => void flush()}
      />
    </div>
  );
}
