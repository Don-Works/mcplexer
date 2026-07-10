import { Card, CardContent } from '@/components/ui/card'

export function SkeletonPage() {
  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="p-4 space-y-3">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="flex items-center gap-4">
              <div className="h-4 w-24 animate-pulse bg-muted" />
              <div className="h-4 w-16 animate-pulse bg-muted" />
              <div className="h-4 w-16 animate-pulse bg-muted ml-auto" />
              <div className="h-4 w-16 animate-pulse bg-muted" />
              <div className="h-4 w-16 animate-pulse bg-muted" />
              <div className="h-4 w-32 animate-pulse bg-muted" />
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  )
}
