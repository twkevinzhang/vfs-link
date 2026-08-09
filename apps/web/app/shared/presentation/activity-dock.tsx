import * as React from 'react';

import { cn } from './utils';

type ActivityDockProps = {
  /** Whether the dock should be rendered. */
  visible: boolean;
  /** Status messages and actions displayed in the dock. */
  children: React.ReactNode;
  /** Accessible name for the dock's status and action region. */
  ariaLabel?: string;
  /** Responsive visibility or positioning overrides for the outer shell. */
  className?: string;
};

/**
 * A viewport-anchored container for temporary page activity such as upload
 * progress, errors, and bulk-selection actions. Its outer shell lets pointer
 * events pass through, while the panel remains fully interactive.
 */
export function ActivityDock({
  visible,
  children,
  ariaLabel = 'Activity and actions',
  className,
}: ActivityDockProps) {
  if (!visible) {
    return null;
  }

  return (
    <div
      className={cn(
        'pointer-events-none fixed inset-x-0 bottom-0 z-30 flex justify-center px-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-4',
        className
      )}
      role="region"
      aria-label={ariaLabel}
    >
      <div
        className={cn(
          'pointer-events-auto w-full max-w-3xl max-h-[min(50dvh,28rem)] overflow-y-auto',
          'rounded-2xl border border-border bg-white shadow-lg'
        )}
      >
        {children}
      </div>
    </div>
  );
}
