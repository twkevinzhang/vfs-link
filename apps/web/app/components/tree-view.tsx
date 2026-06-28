import { ChevronRight, Folder, FolderOpen } from 'lucide-react';
import { useEffect, useState } from 'react';

import { Button } from './ui/button';
import { formatPathDisplayName, normalizePath } from '../lib/format';
import { cn } from '../lib/utils';
import { TreeNode } from '../types/files';

const TREE_BATCH_SIZE = 50;

type TreeViewProps = {
  node?: TreeNode;
  currentPath: string;
  loadingPaths: Set<string>;
  onSelectPath: (path: string) => void;
  onLoadChildren: (path: string) => void;
};

export function TreeView({
  node,
  currentPath,
  loadingPaths,
  onSelectPath,
  onLoadChildren,
}: TreeViewProps) {
  const normalizedCurrentPath = normalizePath(currentPath);
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(
    () => new Set(['/'])
  );

  useEffect(() => {
    setExpandedPaths((previous) => {
      const next = new Set(previous);
      let changed = false;

      for (const path of ancestorDirectoryPaths(normalizedCurrentPath)) {
        if (!next.has(path)) {
          next.add(path);
          changed = true;
        }
      }

      return changed ? next : previous;
    });
  }, [normalizedCurrentPath]);

  if (!node) {
    return null;
  }

  const togglePath = (path: string) => {
    setExpandedPaths((previous) => {
      const next = new Set(previous);
      const isExpanded = next.has(path);
      if (isExpanded) {
        next.delete(path);
      } else {
        next.add(path);
      }

      if (setsEqual(previous, next)) {
        return previous;
      }

      return next;
    });
  };

  const selectPath = (node: TreeNode) => {
    const path = normalizePath(node.path);
    const willExpand = !expandedPaths.has(path);
    const shouldLoadChildren =
      willExpand &&
      node.kind === 'directory' &&
      node.childrenLoaded !== true &&
      !loadingPaths.has(path);
    if (shouldLoadChildren) {
      onLoadChildren(path);
    }
    togglePath(path);
    onSelectPath(path);
  };

  return (
    <nav aria-label="資料夾樹" className="grid min-w-max gap-1">
      <TreeItem
        node={node}
        currentPath={normalizedCurrentPath}
        expandedPaths={expandedPaths}
        loadingPaths={loadingPaths}
        onSelectPath={selectPath}
        depth={0}
      />
    </nav>
  );
}

function TreeItem({
  node,
  currentPath,
  expandedPaths,
  loadingPaths,
  onSelectPath,
  depth,
}: {
  node: TreeNode;
  currentPath: string;
  expandedPaths: Set<string>;
  loadingPaths: Set<string>;
  onSelectPath: (node: TreeNode) => void;
  depth: number;
}) {
  const path = normalizePath(node.path);
  const children = node.children ?? [];
  const isDirectory = node.kind === 'directory';
  const label = formatPathDisplayName(path, node.name);
  const isSelected = path === currentPath;
  const isLoading = loadingPaths.has(path);
  const hasChildren =
    children.length > 0 ||
    node.childrenLoaded !== true ||
    node.hasChildren === true;
  const isExpanded = isDirectory && expandedPaths.has(path);
  const Icon = isExpanded ? FolderOpen : Folder;

  return (
    <div className="grid gap-1">
      <Button
        aria-expanded={isDirectory && hasChildren ? isExpanded : undefined}
        variant={isSelected ? 'secondary' : 'ghost'}
        size="sm"
        className={cn(
          'h-8 w-max min-w-full justify-start px-2 text-left',
          isSelected && 'font-semibold'
        )}
        style={{ paddingLeft: `${Math.min(depth * 14 + 8, 56)}px` }}
        onClick={() => onSelectPath(node)}
        title={path === '/' ? label : path}
      >
        <ChevronRight
          aria-hidden="true"
          className={cn(
            'h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform',
            isExpanded && 'rotate-90',
            !hasChildren && 'opacity-0'
          )}
        />
        <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span className="whitespace-nowrap">
          {label}
          {isLoading ? ' ...' : ''}
        </span>
      </Button>
      {children.length > 0 && isExpanded && (
        <TreeChildren
          nodes={children}
          currentPath={currentPath}
          expandedPaths={expandedPaths}
          loadingPaths={loadingPaths}
          onSelectPath={onSelectPath}
          depth={depth + 1}
        />
      )}
    </div>
  );
}

function TreeChildren({
  nodes,
  currentPath,
  expandedPaths,
  loadingPaths,
  onSelectPath,
  depth,
}: {
  nodes: TreeNode[];
  currentPath: string;
  expandedPaths: Set<string>;
  loadingPaths: Set<string>;
  onSelectPath: (node: TreeNode) => void;
  depth: number;
}) {
  const [visibleCount, setVisibleCount] = useState(TREE_BATCH_SIZE);
  const visibleNodes = nodes.slice(0, visibleCount);
  const hasMore = visibleCount < nodes.length;

  return (
    <div className="grid gap-1">
      {visibleNodes.map((child) => (
        <TreeItem
          key={child.path}
          node={child}
          currentPath={currentPath}
          expandedPaths={expandedPaths}
          loadingPaths={loadingPaths}
          onSelectPath={onSelectPath}
          depth={depth}
        />
      ))}
      {hasMore && (
        <Button
          variant="ghost"
          size="sm"
          className="h-8 w-max min-w-full justify-start px-2 text-left text-muted-foreground"
          style={{ paddingLeft: `${Math.min(depth * 14 + 8, 56)}px` }}
          onClick={() =>
            setVisibleCount((count) =>
              Math.min(count + TREE_BATCH_SIZE, nodes.length)
            )
          }
        >
          顯示更多
        </Button>
      )}
    </div>
  );
}

function ancestorDirectoryPaths(value: string) {
  const normalizedPath = normalizePath(value);
  if (normalizedPath === '/') {
    return ['/'];
  }

  const ancestors = ['/'];
  const parts = normalizedPath.slice(1).split('/').filter(Boolean);
  let current = '';

  for (const part of parts) {
    current += `/${part}`;
    ancestors.push(current);
  }

  return ancestors;
}

function setsEqual(left: Set<string>, right: Set<string>) {
  if (left.size !== right.size) {
    return false;
  }

  for (const value of left) {
    if (!right.has(value)) {
      return false;
    }
  }

  return true;
}
