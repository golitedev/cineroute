import { useT } from '@/lib/i18n';
import React from 'react';
import { Button, ButtonProps } from './ui/button';
import { DownloadIcon, LoaderCircle } from 'lucide-react';
import { QobuzAlbum, QobuzTrack } from '@/lib/qobuz-dl';
import { createDownloadJob, downloadLabel } from '@/lib/download-job';

interface DownloadAlbumButtonProps extends ButtonProps {
    result: QobuzAlbum;
    onOpen?: () => void;
    onClose?: () => void;
}

const DownloadButton = React.forwardRef<HTMLButtonElement, DownloadAlbumButtonProps>(({ result, onOpen, onClose, onClick, ...props }, ref) => {
    const t = useT();
    const [pending, setPending] = React.useState(false);
    const submitting = React.useRef(false);
    return (
        <Button
            {...props}
            ref={ref}
            disabled={props.disabled || pending}
            aria-busy={pending}
            title={downloadLabel('album' in result ? (result as unknown as QobuzTrack).album : result, t)}
            aria-label={downloadLabel('album' in result ? (result as unknown as QobuzTrack).album : result, t)}
            onClick={async (event) => {
                if (submitting.current) return;
                submitting.current = true;
                setPending(true);
                onClick?.(event);
                try {
                    await createDownloadJob(result);
                } finally {
                    submitting.current = false;
                    setPending(false);
                }
            }}
            onFocus={onOpen}
            onBlur={onClose}
        >
            {pending ? <LoaderCircle className='!size-4 animate-spin' /> : <DownloadIcon className='!size-4' />}
        </Button>
    );
});
DownloadButton.displayName = 'DownloadAlbumButton';
export default DownloadButton;
