import { ChevronRight, File, Folder, FolderOpen } from 'lucide-react';
import { useEffect, useState } from 'react';

import { Button } from './ui/button';
import { normalizePath } from '../lib/format';
import { cn } from '../lib/utils';
import { TreeNode } from '../types/files';

const TREE_BATCH_SIZE = 50;

type TreeViewProps = {
  node?: TreeNode;
  currentPath: string;
  selectedFilePath?: string;
  onSelectPath: (path: string) => void;
  onSelectFile: (node: TreeNode) => void;
};

export function TreeView({
  node,
  currentPath,
  selectedFilePath,
  onSelectPath,
  onSelectFile,
}: TreeViewProps) {
  const normalizedCurrentPath = normalizePath(currentPath);
  const normalizedSelectedFilePath =
    selectedFilePath && normalizePath(selectedFilePath);
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

  const expandPath = (path: string) => {
    setExpandedPaths((previous) => {
      if (previous.has(path)) {
        return previous;
      }

      const next = new Set(previous);
      next.add(path);
      return next;
    });
  };

  const selectPath = (path: string) => {
    expandPath(path);
    onSelectPath(path);
  };

  return (
    <nav aria-label="資料夾樹" className="grid min-w-max gap-1">
      <TreeItem
        node={node}
        currentPath={normalizedCurrentPath}
        selectedFilePath={normalizedSelectedFilePath}
        expandedPaths={expandedPaths}
        onSelectPath={selectPath}
        onSelectFile={onSelectFile}
        depth={0}
      />
    </nav>
  );
}

function TreeItem({
  node,
  currentPath,
  selectedFilePath,
  expandedPaths,
  onSelectPath,
  onSelectFile,
  depth,
}: {
  node: TreeNode;
  currentPath: string;
  selectedFilePath?: string;
  expandedPaths: Set<string>;
  onSelectPath: (path: string) => void;
  onSelectFile: (node: TreeNode) => void;
  depth: number;
}) {
  const path = normalizePath(node.path);
  const children = node.children ?? [];
  const isDirectory = node.kind === 'directory';
  const isSelected = isDirectory
    ? path === currentPath
    : path === selectedFilePath;
  const hasChildren = children.length > 0;
  const isExpanded = isDirectory && expandedPaths.has(path);
  const Icon = !isDirectory ? File : isExpanded ? FolderOpen : Folder;

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
        onClick={() => (isDirectory ? onSelectPath(path) : onSelectFile(node))}
        title={path}
      >
        {isDirectory ? (
          <ChevronRight
            aria-hidden="true"
            className={cn(
              'h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform',
              isExpanded && 'rotate-90',
              !hasChildren && 'opacity-0'
            )}
          />
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span className="whitespace-nowrap">{node.name || '/'}</span>
      </Button>
      {hasChildren && isExpanded && (
        <TreeChildren
          nodes={children}
          currentPath={currentPath}
          selectedFilePath={selectedFilePath}
          expandedPaths={expandedPaths}
          onSelectPath={onSelectPath}
          onSelectFile={onSelectFile}
          depth={depth + 1}
        />
      )}
    </div>
  );
}

function TreeChildren({
  nodes,
  currentPath,
  selectedFilePath,
  expandedPaths,
  onSelectPath,
  onSelectFile,
  depth,
}: {
  nodes: TreeNode[];
  currentPath: string;
  selectedFilePath?: string;
  expandedPaths: Set<string>;
  onSelectPath: (path: string) => void;
  onSelectFile: (node: TreeNode) => void;
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
          selectedFilePath={selectedFilePath}
          expandedPaths={expandedPaths}
          onSelectPath={onSelectPath}
          onSelectFile={onSelectFile}
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
