import sys
import time
import yaml
from pr_test_case import PullRequestOperation
from compare import compare_repo_members_with_maintainers


def main():
    with open('config.yaml', 'r') as f:
        # yaml文件读取配置
        info = yaml.load(f.read())['test case']
        owner = info[0]['owner']
        repo = info[1]['repo']
        local_owner = info[2]['local_owner']
        # 实例化
        pr = PullRequestOperation(owner, repo, local_owner)
        # 在OWNERS中追加maintainer
        with open('community/sig/Infrastructure', 'a') as f:
            f.write('  - liuqi469227928')
        # push代码
        pr.git_push()
        # 提pr并获取pr编号
        number = pr.pull_request()
        time.sleep(5)
        # 添加'lgtm'标签
        pr.add_labels_2_pr(number, '["lgtm"]')
        # 评论/approve
        pr.comment(number, '/approve')
        # 获取pr所有标签
        labels = pr.get_all_labels(number)
        print('labels: {}'.format(labels))
        # 查看pr是否已经合入
        code = pr.get_pr_status(number)
        if code == 200:
            print('pr has been merged')
            time.sleep(90)
            compare_repo_members_with_maintainers(sig_name='Infrastructure')
        else:
            print('pr is already open')
            sys.exit(1)


if __name__ == '__main__':
    main()
