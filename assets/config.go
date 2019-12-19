package assets

const (
	DefaultConfig = `
    triggers:
      - name: on-sync-status-unknown
        condition: app.status.sync.status == 'Unknown'
        template: app-sync-status

      - name: on-sync-failed
        condition: app.status.operationState.phase in ['Error', 'Failed']
        template: app-sync-failed

      - name: on-health-degraded
        condition: app.status.health.status == 'Degraded'
        template: app-health-degraded

    templates:
      - name: app-sync-status
        title: Application {{.app.metadata.name}} sync status is {{.app.status.sync.status}}
        body: |
          Application {{.app.metadata.name}} sync is {{.app.status.sync.status}}.
          Application details: {{.context.argocdURL}}/applications/{{.app.metadata.name}}.

      - name: app-sync-failed
        title: Failed to sync application {{.app.metadata.name}}.
        body: |
          The sync operation of application {{.app.metadata.name}} has failed at {{.app.status.operationState.finishedAt}} with the following error: {{.app.status.operationState.message}}
          Sync operation details is available at: {{.context.argocdURL}}/applications/{{.app.metadata.name}}?operation=true .

      - name: app-health-degraded
        title: Application {{.app.metadata.name}} has degraded.
        body: |
          Application {{.app.metadata.name}} has degraded.
          Application details: {{.context.argocdURL}}/applications/{{.app.metadata.name}}.`
)
