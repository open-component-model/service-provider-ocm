package runtime

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/stretchr/testify/assert"
)

func TestPCReconciler_Reconcile(t *testing.T) {
	tests := []struct {
		name           string
		providerConfig ProviderConfig
		req            ctrl.Request
		want           ctrl.Result
		wantObject     bool
		wantErr        bool
	}{
		{
			name: "test notify with provider config",
			providerConfig: &fakeProviderConfigImpl{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			req: ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "test",
				},
			},
			want:       ctrl.Result{},
			wantObject: true,
			wantErr:    false,
		},
		{
			name: "test notify on provider config marked for deletion",
			providerConfig: &fakeProviderConfigImpl{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
					Finalizers: []string{"pc-finalizer"},
				},
			},
			req: ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "test",
				},
			},
			want:       ctrl.Result{},
			wantObject: false,
			wantErr:    false,
		},
		{
			name: "test notify on provider config not found",
			providerConfig: &fakeProviderConfigImpl{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			req: ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "notfound",
				},
			},
			want:       ctrl.Result{},
			wantObject: false,
			wantErr:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewPCReconciler("test", func() *fakeProviderConfigImpl {
				return &fakeProviderConfigImpl{}
			}).
				WithPlatformCluster(createFakeCluster(t, "platform", tt.providerConfig)).
				WithUpdateChannel(make(chan event.GenericEvent, 1))
			got, gotErr := r.Reconcile(context.Background(), tt.req)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("Reconcile() failed: %v", gotErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("Reconcile() succeeded unexpectedly")
			}
			assert.Equal(t, tt.want, got)
			pcUpdate := <-r.providerUpdateChannel
			assert.Equal(t, tt.wantObject, pcUpdate.Object != nil)
		})
	}
}
