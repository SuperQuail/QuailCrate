import { useEffect, useRef, useState } from 'react';
import {
  Disc3,
  ListMusic,
  Loader2,
  Pause,
  Play,
  Repeat,
  Repeat1,
  Shuffle,
  SkipBack,
  SkipForward,
} from 'lucide-react';
import { formatMs, type MusicState, type Track } from '../types';
import { VolumeControl } from './VolumeControl';

interface PlayerDockProps {
  state: MusicState;
  currentTrack: Track | null;
  queueOpen: boolean;
  onTogglePlay: () => void;
  onPrev: () => void;
  onNext: () => void;
  onCycleMode: () => void;
  onSeekCommit: (positionMs: number) => void;
  onVolumeChange: (volume: number, muted: boolean) => Promise<void>;
  onToggleQueue: () => void;
}

export function PlayerDock({
  state,
  currentTrack,
  queueOpen,
  onTogglePlay,
  onPrev,
  onNext,
  onCycleMode,
  onSeekCommit,
  onVolumeChange,
  onToggleQueue,
}: PlayerDockProps) {
  const pb = state.playback;
  const durationMs = pb.durationMs > 0 ? pb.durationMs : currentTrack?.durationMs || 0;

  const [dragging, setDragging] = useState(false);
  const [dragMs, setDragMs] = useState(0);
  const [livePosMs, setLivePosMs] = useState(pb.positionMs);
  const syncRef = useRef<{ baseMs: number; at: number }>({ baseMs: pb.positionMs, at: Date.now() });

  // Resync smooth clock whenever host pushes updated playback state.
  useEffect(() => {
    syncRef.current = { baseMs: pb.positionMs, at: Date.now() };
    setLivePosMs(pb.positionMs);
  }, [pb.positionMs, pb.trackId, pb.status]);

  // Smooth local progress interpolation between 1s host polls while playing.
  useEffect(() => {
    if (pb.status !== 'playing') return;
    const timer = window.setInterval(() => {
      const elapsed = Date.now() - syncRef.current.at;
      const next = syncRef.current.baseMs + elapsed;
      setLivePosMs(durationMs > 0 ? Math.min(durationMs, next) : next);
    }, 250);
    return () => window.clearInterval(timer);
  }, [pb.status, durationMs]);

  const shownMs = dragging ? dragMs : livePosMs;
  const isPlaying = pb.status === 'playing';
  const isLoading = pb.status === 'loading';

  const modeMeta =
    pb.mode === 'shuffle'
      ? { icon: <Shuffle size={15} />, label: '随机播放', active: true }
      : pb.mode === 'repeat_one'
        ? { icon: <Repeat1 size={15} />, label: '单曲循环', active: true }
        : { icon: <Repeat size={15} />, label: '顺序播放', active: false };

  let subText = '选择任意曲目开始流式播放';
  let isErr = false;
  if (pb.status === 'error' && pb.error) {
    subText = pb.error;
    isErr = true;
  } else if (currentTrack) {
    subText = isLoading
      ? `${currentTrack.artist || 'Bilibili'} · 正在流式缓冲…`
      : `${currentTrack.artist || 'Bilibili'} · ${currentTrack.bvid}`;
  }

  return (
    <footer className="player-dock">
      <div className="dock-left">
        <div className="dock-cover swatch-accent">
          <Disc3 size={22} />
        </div>
        <div className="dock-track-meta">
          <div className="dock-title" title={currentTrack?.title || '未播放曲目'}>
            {currentTrack?.title || '未播放曲目'}
          </div>
          <div className={`dock-sub${isErr ? ' error' : ''}`} title={subText}>
            {subText}
          </div>
        </div>
      </div>

      <div className="dock-center">
        <div className="transport-row">
          <button
            type="button"
            className={`ctrl-btn${modeMeta.active ? ' active' : ''}`}
            title={`播放模式：${modeMeta.label}`}
            onClick={onCycleMode}
          >
            {modeMeta.icon}
          </button>
          <button type="button" className="ctrl-btn" title="上一首" onClick={onPrev}>
            <SkipBack size={16} />
          </button>
          <button
            type="button"
            className="play-btn"
            title={isPlaying ? '暂停' : '播放'}
            onClick={onTogglePlay}
          >
            {isLoading ? (
              <Loader2 size={16} />
            ) : isPlaying ? (
              <Pause size={16} fill="currentColor" />
            ) : (
              <Play size={16} fill="currentColor" />
            )}
          </button>
          <button type="button" className="ctrl-btn" title="下一首" onClick={onNext}>
            <SkipForward size={16} />
          </button>
        </div>

        <div className="seek-row">
          <span className="time-code">{formatMs(shownMs)}</span>
          <input
            type="range"
            className="seek-slider"
            min={0}
            max={Math.max(1, durationMs)}
            value={Math.min(Math.max(0, Math.round(shownMs)), Math.max(1, durationMs))}
            disabled={!currentTrack || durationMs <= 0}
            onPointerDown={() => {
              setDragging(true);
              setDragMs(livePosMs);
            }}
            onChange={(e) => {
              const v = Number(e.target.value);
              setDragMs(v);
              if (!dragging) {
                setLivePosMs(v);
              }
            }}
            onPointerUp={(e) => {
              const v = Number(e.currentTarget.value);
              setDragging(false);
              setLivePosMs(v);
              syncRef.current = { baseMs: v, at: Date.now() };
              onSeekCommit(v);
            }}
            onKeyUp={(e) => {
              if (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End') {
                const v = Number(e.currentTarget.value);
                setLivePosMs(v);
                syncRef.current = { baseMs: v, at: Date.now() };
                onSeekCommit(v);
              }
            }}
          />
          <span className="time-code">{formatMs(durationMs)}</span>
        </div>
      </div>

      <div className="dock-right">
        {pb.qualityLabel && <span className="quality-badge">{pb.qualityLabel}</span>}

        <VolumeControl volume={pb.volume} muted={pb.muted} onCommit={onVolumeChange} />

        <button
          type="button"
          className={`queue-trigger${queueOpen ? ' active' : ''}`}
          title="播放队列"
          onClick={onToggleQueue}
        >
          <ListMusic size={14} />
          <span>{state.queue.length}</span>
        </button>
      </div>
    </footer>
  );
}
