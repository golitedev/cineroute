import { useT } from '@/lib/i18n';
import React from 'react';
import { Button, ButtonProps } from './ui/button';
import { DownloadIcon } from 'lucide-react';
import { QobuzAlbum, QobuzTrack } from '@/lib/qobuz-dl';
import { createDownloadJob, downloadLabel } from '@/lib/download-job';

interface DownloadAlbumButtonProps extends ButtonProps {
    result: QobuzAlbum;
    onOpen?: () => void;
    onClose?: () => void;
}

const DownloadButton = React.forwardRef<HTMLButtonElement, DownloadAlbumButtonProps>(({ result, onOpen, onClose, onClick, ...props }, ref) => {
    const t = useT();
    return (
        <Button
            {...props}
            ref={ref}
            title={downloadLabel('album' in result ? (result as unknown as QobuzTrack).album : result, t)}
            aria-label={downloadLabel('album' in result ? (result as unknown as QobuzTrack).album : result, t)}
            onClick={(event) => {
                void createDownloadJob(result);
                onClick?.(event);
            }}
            onFocus={onOpen}
            onBlur={onClose}
        >
            <DownloadIcon className='!size-4' />
        </Button>
    );
});
DownloadButton.displayName = 'DownloadAlbumButton';
export default DownloadButton;
