export namespace main {
	
	export class ContextConnectionResult {
	    context: string;
	    success: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ContextConnectionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.context = source["context"];
	        this.success = source["success"];
	        this.error = source["error"];
	    }
	}
	export class FavoriteViewGVK {
	    group: string;
	    version: string;
	    kind: string;
	
	    static createFrom(source: any = {}) {
	        return new FavoriteViewGVK(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.version = source["version"];
	        this.kind = source["kind"];
	    }
	}
	export class FavoriteViewResponse {
	    id: string;
	    name: string;
	    gvk: FavoriteViewGVK;
	    fields: string[][];
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new FavoriteViewResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.gvk = this.convertValues(source["gvk"], FavoriteViewGVK);
	        this.fields = source["fields"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MultiClusterGVK {
	    group: string;
	    version: string;
	    kind: string;
	    shortNames: string[];
	    contexts: string[];
	    allCount: number;
	
	    static createFrom(source: any = {}) {
	        return new MultiClusterGVK(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.version = source["version"];
	        this.kind = source["kind"];
	        this.shortNames = source["shortNames"];
	        this.contexts = source["contexts"];
	        this.allCount = source["allCount"];
	    }
	}
	export class SetSelectedFieldsResult {
	    needsResync: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SetSelectedFieldsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.needsResync = source["needsResync"];
	    }
	}
	export class TreeNode {
	    name: string;
	    type: string;
	    fullPath: string[];
	    level: number;
	    children: TreeNode[];
	
	    static createFrom(source: any = {}) {
	        return new TreeNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.fullPath = source["fullPath"];
	        this.level = source["level"];
	        this.children = this.convertValues(source["children"], TreeNode);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

