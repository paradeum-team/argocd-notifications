package argocd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/argoproj-labs/argocd-notifications/expr/shared"
	"github.com/argoproj/argo-cd/v2/common"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v2/reposerver/apiclient"
	"github.com/argoproj/argo-cd/v2/util/db"
	"github.com/argoproj/argo-cd/v2/util/env"
	"github.com/argoproj/argo-cd/v2/util/settings"
	"github.com/argoproj/argo-cd/v2/util/tls"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

//go:generate mockgen -destination=./mocks/service.go -package=mocks github.com/argoproj-labs/argocd-notifications/shared/argocd Service

type Service interface {
	GetCommitMetadata(ctx context.Context, repoURL string, commitSHA string) (*shared.CommitMetadata, error)
	GetAppDetails(ctx context.Context, appSource *v1alpha1.ApplicationSource) (*shared.AppDetail, error)
}

func NewArgoCDService(clientset kubernetes.Interface, namespace string, repoServerAddress string, disableTLS bool, strictValidation bool) (*argoCDService, error) {
	ctx, cancel := context.WithCancel(context.Background())
	settingsMgr := settings.NewSettingsManager(ctx, clientset, namespace)
	tlsConfig := apiclient.TLSConfiguration{
		DisableTLS:       disableTLS,
		StrictValidation: strictValidation,
	}
	if !disableTLS && strictValidation {
		pool, err := tls.LoadX509CertPool(
			fmt.Sprintf("%s/reposerver/tls/tls.crt", env.StringFromEnv(common.EnvAppConfigPath, common.DefaultAppConfigPath)),
			fmt.Sprintf("%s/reposerver/tls/ca.crt", env.StringFromEnv(common.EnvAppConfigPath, common.DefaultAppConfigPath)),
		)
		if err != nil {
			cancel()
			return nil, err
		}
		tlsConfig.Certificates = pool
	}
	repoClientset := apiclient.NewRepoServerClientset(repoServerAddress, 5, tlsConfig)
	closer, repoClient, err := repoClientset.NewRepoServerClient()
	if err != nil {
		cancel()
		return nil, err
	}

	dispose := func() {
		cancel()
		if err := closer.Close(); err != nil {
			log.Warnf("Failed to close repo server connection: %v", err)
		}
	}
	return &argoCDService{settingsMgr: settingsMgr, namespace: namespace, repoServerClient: repoClient, dispose: dispose}, nil
}

type argoCDService struct {
	clientset        kubernetes.Interface
	namespace        string
	settingsMgr      *settings.SettingsManager
	repoServerClient apiclient.RepoServerServiceClient
	dispose          func()
}

func (svc *argoCDService) GetCommitMetadata(ctx context.Context, repoURL string, commitSHA string) (*shared.CommitMetadata, error) {
	argocdDB := db.NewDB(svc.namespace, svc.settingsMgr, svc.clientset)
	repo, err := argocdDB.GetRepository(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	metadata, err := svc.repoServerClient.GetRevisionMetadata(ctx, &apiclient.RepoServerRevisionMetadataRequest{
		Repo:     repo,
		Revision: commitSHA,
	})
	if err != nil {
		return nil, err
	}
	return &shared.CommitMetadata{
		Message: metadata.Message,
		Author:  metadata.Author,
		Date:    metadata.Date.Time,
		Tags:    metadata.Tags,
	}, nil
}

func (svc *argoCDService) getKustomizeOptions(source *v1alpha1.ApplicationSource) (*v1alpha1.KustomizeOptions, error) {
	kustomizeSettings, err := svc.settingsMgr.GetKustomizeSettings()
	if err != nil {
		return nil, err
	}
	return kustomizeSettings.GetOptions(*source)
}

func (svc *argoCDService) GetAppDetails(ctx context.Context, appSource *v1alpha1.ApplicationSource) (*shared.AppDetail, error) {
	argocdDB := db.NewDB(svc.namespace, svc.settingsMgr, svc.clientset)
	repo, err := argocdDB.GetRepository(ctx, appSource.RepoURL)
	if err != nil {
		return nil, err
	}
	helmRepos, err := argocdDB.ListHelmRepositories(ctx)
	if err != nil {
		return nil, err
	}
	kustomizeOptions, err := svc.getKustomizeOptions(appSource)
	if err != nil {
		return nil, err
	}
	appDetail, err := svc.repoServerClient.GetAppDetails(ctx, &apiclient.RepoServerAppDetailsQuery{
		Repo:             repo,
		Source:           appSource,
		Repos:            helmRepos,
		KustomizeOptions: kustomizeOptions,
	})
	if err != nil {
		return nil, err
	}
	var has *shared.HelmAppSpec
	if appDetail.Helm != nil {
		if appSource.Helm.Parameters != nil {
			for _, overrideParam := range appSource.Helm.Parameters {
				for _, defaultParam := range appDetail.Helm.Parameters {
					if overrideParam.Name == defaultParam.Name {
						defaultParam.Value = overrideParam.Value
						defaultParam.ForceString = overrideParam.ForceString
					}
				}
			}
		}

		if appSource.Helm.Values != "" {
			output := map[string]string{}
			var valuesMap map[string]interface{}
			if err := json.Unmarshal([]byte(appSource.Helm.Values), valuesMap); err != nil {
				return nil, fmt.Errorf("failed to parse appSource.Helm.Values: %s", err)
			}
			flatVals(valuesMap, output)

			for i := range has.Parameters {
				if v, ok := output[has.Parameters[i].Name]; ok {
					has.Parameters[i].Value = v
					delete(output, has.Parameters[i].Name)
					break
				}
			}

			for k, v := range output {
				appDetail.Helm.Parameters = append(appDetail.Helm.Parameters, &v1alpha1.HelmParameter{Name: k, Value: v})
			}
		}

		has = &shared.HelmAppSpec{
			Name:           appDetail.Helm.Name,
			ValueFiles:     appSource.Helm.ValueFiles,
			Parameters:     appDetail.Helm.Parameters,
			Values:         appSource.Helm.Values,
			FileParameters: appDetail.Helm.FileParameters,
		}
	}
	return &shared.AppDetail{
		Type:      appDetail.Type,
		Helm:      has,
		Ksonnet:   appDetail.Ksonnet,
		Kustomize: appDetail.Kustomize,
		Directory: appDetail.Directory,
	}, nil
}

func (svc *argoCDService) Close() {
	svc.dispose()
}

func flatVals(input interface{}, output map[string]string, prefixes ...string) {
	switch i := input.(type) {
	case map[string]interface{}:
		for k, v := range i {
			flatVals(v, output, append(prefixes, k)...)
		}
	case []interface{}:
		p := append([]string(nil), prefixes...)
		for j, v := range i {
			flatVals(v, output, append(p[0:len(p)-1], fmt.Sprintf("%s[%v]", prefixes[len(p)-1], j))...)
		}
	default:
		output[strings.Join(prefixes, ".")] = fmt.Sprintf("%v", i)
	}
}
