import { useEffect, useRef } from 'react';
import QRCode from 'qrcode';
import { LogOut, QrCode, RefreshCw, X } from 'lucide-react';
import type { MusicState, QualityPref } from '../types';

interface AccountModalProps {
  open: boolean;
  state: MusicState;
  onClose: () => void;
  onStartLogin: () => void;
  onCancelLogin: () => void;
  onRefreshAccount: () => void;
  onLogout: () => void;
  onSelectQuality: (q: QualityPref) => void;
}

const QUALITY_OPTIONS: { id: QualityPref; title: string; desc: string }[] = [
  { id: 'flac', title: 'Hi-Res / FLAC', desc: '大会员无损优先（自动回退）' },
  { id: '192k', title: '高品 192K AAC', desc: '推荐 · 流式秒开平衡清晰' },
  { id: '132k', title: '标准 132K AAC', desc: '通用稳定流畅' },
  { id: '64k', title: '省流 64K AAC', desc: '低带宽优先' },
];

export function AccountModal({
  open,
  state,
  onClose,
  onStartLogin,
  onCancelLogin,
  onRefreshAccount,
  onLogout,
  onSelectQuality,
}: AccountModalProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const acc = state.account;
  const login = state.login;

  const hasQr = Boolean(login.qrUrl) && (login.phase === 'waiting_scan' || login.phase === 'scanned_confirm');

  useEffect(() => {
    if (!open || acc.loggedIn || !hasQr || !canvasRef.current) return;
    QRCode.toCanvas(canvasRef.current, login.qrUrl, {
      width: 168,
      margin: 1,
      errorCorrectionLevel: 'L',
      color: { dark: '#11111b', light: '#ffffff' },
    }).catch(() => {});
  }, [open, acc.loggedIn, hasQr, login.qrUrl]);

  if (!open) return null;

  const showOverlay = !hasQr || login.phase === 'scanned_confirm';
  const polling =
    login.phase === 'generating' ||
    login.phase === 'waiting_scan' ||
    login.phase === 'scanned_confirm';

  let overlayLabel = '点击下方按钮获取登录二维码';
  if (login.phase === 'generating') overlayLabel = '正在向 Bilibili 请求二维码…';
  else if (login.phase === 'scanned_confirm') overlayLabel = '已扫码，请在手机端确认登录';
  else if (login.phase === 'confirming') overlayLabel = '登录成功，正在同步账号…';
  else if (login.phase === 'expired') overlayLabel = '二维码已过期，请重新获取';
  else if (login.phase === 'error') overlayLabel = '获取二维码失败';

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <h2 className="modal-title">Bilibili 账号与音质</h2>
          <button type="button" className="row-btn" onClick={onClose}>
            <X size={15} />
          </button>
        </div>

        <div className="modal-body">
          {acc.loggedIn ? (
            <div className="profile-card">
              <div className="profile-left">
                <div className="account-avatar account-avatar-lg">
                  {(acc.nickname || 'B').slice(0, 1).toUpperCase()}
                </div>
                <div>
                  <div className="profile-name-row">
                    <span className="profile-name">{acc.nickname || 'Bilibili 用户'}</span>
                    {acc.vip && <span className="vip-badge">大会员</span>}
                  </div>
                  <div className="account-sub">
                    UID {acc.mid} · LV{acc.level}
                  </div>
                </div>
              </div>
              <div className="header-actions">
                <button type="button" className="btn btn-sm" onClick={onRefreshAccount}>
                  <RefreshCw size={12} />
                  <span>校验</span>
                </button>
                <button type="button" className="btn btn-sm btn-danger" onClick={onLogout}>
                  <LogOut size={12} />
                  <span>退出</span>
                </button>
              </div>
            </div>
          ) : (
            <div className="qr-panel">
              <div className="qr-frame">
                <canvas ref={canvasRef} className="qr-canvas" width={168} height={168} />
                {showOverlay && (
                  <div className="qr-overlay">
                    <QrCode size={24} />
                    <span>{overlayLabel}</span>
                  </div>
                )}
              </div>
              <div className={`qr-status${login.phase === 'error' ? ' error' : ''}`}>
                {login.message || '使用哔哩哔哩 App 扫码登录，Cookie 仅保存在宿主加密会话中'}
              </div>
              <div className="qr-actions">
                <button type="button" className="btn btn-primary" onClick={onStartLogin}>
                  <RefreshCw size={13} />
                  <span>{hasQr ? '刷新二维码' : '获取二维码'}</span>
                </button>
                {polling && (
                  <button type="button" className="btn" onClick={onCancelLogin}>
                    <span>取消</span>
                  </button>
                )}
              </div>
            </div>
          )}

          <div className="field">
            <span className="field-label">偏好音质（自动回退到当前账号可播放的最高音轨）</span>
            <div className="quality-grid">
              {QUALITY_OPTIONS.map((opt) => (
                <button
                  key={opt.id}
                  type="button"
                  className={`quality-opt${state.preferredQuality === opt.id ? ' active' : ''}`}
                  onClick={() => onSelectQuality(opt.id)}
                >
                  <div className="quality-title">{opt.title}</div>
                  <div className="quality-desc">{opt.desc}</div>
                </button>
              ))}
            </div>
          </div>
        </div>

        <div className="modal-footer">
          <button type="button" className="btn" onClick={onClose}>
            完成
          </button>
        </div>
      </div>
    </div>
  );
}
