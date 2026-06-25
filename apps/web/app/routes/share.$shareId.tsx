import {
  AlertCircle,
  ArrowLeft,
  CheckCircle2,
  Copy,
  Loader2,
  RefreshCcw,
  Send,
  UploadCloud,
} from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router';

import { Alert } from '../components/ui/alert';
import { Badge } from '../components/ui/badge';
import { Button } from '../components/ui/button';
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '../components/ui/card';
import { Skeleton } from '../components/ui/skeleton';
import { getShare, startShare } from '../lib/api';
import { formatBytes, formatDate } from '../lib/format';
import { ShareRecord, ShareStatus } from '../types/share';

const terminalStatuses = new Set<ShareStatus>([
  'completed',
  'notified',
  'notification_failed',
  'email_sent',
  'failed',
  'email_failed',
]);

const statusLabels = {
  draft: 'draft',
  uploading: 'uploading',
  completed: 'completed',
  notified: 'notified',
  notification_failed: 'notification failed',
  email_sent: 'email sent',
  failed: 'failed',
  email_failed: 'email failed',
} satisfies Record<ShareStatus, string>;

export default function ShareRoute() {
  const { shareId } = useParams();
  const [share, setShare] = useState<ShareRecord>();
  const [loading, setLoading] = useState(true);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string>();
  const [copied, setCopied] = useState(false);

  const load = useCallback(async () => {
    if (!shareId) {
      setError('Missing share id');
      setLoading(false);
      return;
    }
    try {
      const nextShare = await getShare(shareId);
      setShare(nextShare);
      setError(undefined);
    } catch (loadError) {
      setError(
        loadError instanceof Error ? loadError.message : 'Unable to load share'
      );
    } finally {
      setLoading(false);
    }
  }, [shareId]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!share || terminalStatuses.has(share.status)) {
      return;
    }
    const timer = window.setInterval(() => {
      void load();
    }, 1500);
    return () => window.clearInterval(timer);
  }, [load, share]);

  async function handleStart() {
    if (!shareId) {
      return;
    }
    setStarting(true);
    setError(undefined);
    try {
      const nextShare = await startShare(shareId);
      setShare(nextShare);
    } catch (startError) {
      setError(
        startError instanceof Error
          ? startError.message
          : 'Unable to start share'
      );
    } finally {
      setStarting(false);
    }
  }

  async function handleCopy() {
    if (!share?.shareUrl) {
      return;
    }
    await navigator.clipboard.writeText(share.shareUrl);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  }

  const canStart =
    share?.status === 'draft' ||
    share?.status === 'failed' ||
    share?.status === 'notification_failed' ||
    share?.status === 'email_failed';
  const isBusy = share?.status === 'uploading' || starting;
  const isSuccessful =
    share?.status === 'completed' ||
    share?.status === 'notified' ||
    share?.status === 'email_sent';

  return (
    <main className="min-h-screen bg-background text-foreground">
      <div className="mx-auto flex min-h-screen w-full max-w-5xl flex-col gap-6 px-4 py-5 sm:px-6 lg:px-8">
        <header className="flex flex-col gap-4 border-b border-border pb-5 md:flex-row md:items-end md:justify-between">
          <div className="grid gap-2">
            <Link
              to="/"
              className="inline-flex w-fit items-center gap-2 text-sm text-muted-foreground hover:text-foreground"
            >
              <ArrowLeft aria-hidden="true" className="h-4 w-4" />
              File browser
            </Link>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="secondary">GCS share</Badge>
              <Badge variant="outline">
                {share ? statusLabels[share.status] : 'loading'}
              </Badge>
            </div>
            <h1 className="text-2xl font-semibold tracking-normal sm:text-3xl">
              File share
            </h1>
          </div>
          <Button
            variant="outline"
            onClick={() => void load()}
            disabled={loading}
          >
            <RefreshCcw aria-hidden="true" className="h-4 w-4" />
            Refresh
          </Button>
        </header>

        {error && (
          <Alert className="border-destructive/35 bg-white text-destructive">
            <div className="flex items-start gap-3">
              <AlertCircle
                aria-hidden="true"
                className="mt-0.5 h-5 w-5 shrink-0"
              />
              <div className="grid gap-1">
                <p className="font-semibold">Share error</p>
                <p className="text-sm text-foreground">{error}</p>
              </div>
            </div>
          </Alert>
        )}

        {loading && !share ? (
          <div className="grid gap-4">
            <Skeleton className="h-32" />
            <Skeleton className="h-56" />
          </div>
        ) : share ? (
          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
            <section className="grid gap-4">
              <Card>
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <UploadCloud aria-hidden="true" className="h-5 w-5" />
                    Upload target
                  </CardTitle>
                </CardHeader>
                <CardContent className="grid gap-4">
                  <Field label="File" value={share.logicPath} />
                  <Field label="Size" value={formatBytes(share.size)} />
                  <Field label="Destination" value={share.destinationUrl} />
                  <Field label="Object" value={share.destinationObject} />
                </CardContent>
              </Card>

              <Card>
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <Send aria-hidden="true" className="h-5 w-5" />
                    Telegram notification
                  </CardTitle>
                </CardHeader>
                <CardContent className="grid gap-4">
                  <div className="rounded-md border border-border bg-muted/40 p-3 text-sm">
                    <p className="font-medium">Target chat</p>
                    <p className="mt-1 break-all text-muted-foreground">
                      {share.notificationTarget || 'Configured on server'}
                    </p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      onClick={() => void handleStart()}
                      disabled={!canStart || isBusy}
                    >
                      {isBusy ? (
                        <Loader2
                          aria-hidden="true"
                          className="h-4 w-4 animate-spin"
                        />
                      ) : (
                        <Send aria-hidden="true" className="h-4 w-4" />
                      )}
                      {isBusy ? 'Uploading' : 'Confirm upload'}
                    </Button>
                    {isSuccessful && (
                      <Button
                        variant="outline"
                        onClick={() => void handleCopy()}
                      >
                        <Copy aria-hidden="true" className="h-4 w-4" />
                        {copied ? 'Copied' : 'Copy link'}
                      </Button>
                    )}
                  </div>
                </CardContent>
              </Card>
            </section>

            <aside className="rounded-lg border border-border bg-white p-5">
              <div className="grid gap-5">
                <StatusRow
                  active={Boolean(share)}
                  done={true}
                  label="Draft"
                  detail={formatDate(share.createdAt)}
                />
                <StatusRow
                  active={share.status === 'uploading'}
                  done={
                    isSuccessful ||
                    share.status === 'notification_failed' ||
                    share.status === 'email_failed'
                  }
                  label="Upload"
                  detail={
                    share.completedAt
                      ? formatDate(share.completedAt)
                      : share.status
                  }
                />
                <StatusRow
                  active={share.status === 'notified'}
                  done={share.status === 'notified'}
                  label="Telegram"
                  detail={
                    share.notifiedAt
                      ? formatDate(share.notifiedAt)
                      : share.notificationTarget || share.email || '-'
                  }
                />
                {share.error && (
                  <div className="rounded-md border border-destructive/35 p-3 text-sm text-destructive">
                    {share.error}
                  </div>
                )}
                {isSuccessful && (
                  <a
                    href={share.shareUrl}
                    className="break-all rounded-md border border-border bg-muted/50 p-3 text-sm text-accent hover:bg-muted"
                    target="_blank"
                    rel="noreferrer"
                  >
                    {share.shareUrl}
                  </a>
                )}
              </div>
            </aside>
          </div>
        ) : null}
      </div>
    </main>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1">
      <dt className="text-xs font-semibold uppercase tracking-normal text-muted-foreground">
        {label}
      </dt>
      <dd className="break-all text-sm">{value}</dd>
    </div>
  );
}

function StatusRow({
  active,
  done,
  label,
  detail,
}: {
  active: boolean;
  done: boolean;
  label: string;
  detail: string;
}) {
  return (
    <div className="flex items-start gap-3">
      <div className="mt-0.5">
        {done ? (
          <CheckCircle2 aria-hidden="true" className="h-5 w-5 text-[#11615a]" />
        ) : active ? (
          <Loader2
            aria-hidden="true"
            className="h-5 w-5 animate-spin text-accent"
          />
        ) : (
          <div className="h-5 w-5 rounded-full border border-border" />
        )}
      </div>
      <div className="min-w-0">
        <p className="text-sm font-semibold">{label}</p>
        <p className="break-all text-xs text-muted-foreground">{detail}</p>
      </div>
    </div>
  );
}
